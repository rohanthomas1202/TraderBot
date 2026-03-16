"""
GFS Ensemble Reforecast Data Fetcher

Downloads GFS ensemble reforecast data from NOAA NOMADS for target stations.
Extracts point values at specific lat/lon coordinates to avoid downloading
full global grids (which would be hundreds of GB).

Data source: NOAA NOMADS GFS ensemble (GEFS reforecast)
- URL pattern: https://nomads.ncep.noaa.gov/pub/data/nccf/com/gefs/prod/
- Free, no auth required
- GRIB2 format, 0.25 degree resolution
- 31 ensemble members (control + 30 perturbations)
- 6-hour cycles: 00, 06, 12, 18 UTC

For historical reforecasts (2000-2019), use the GEFS v12 reforecast dataset:
- https://noaa-gefs-retrospective.s3.amazonaws.com/
- Available via AWS Open Data (free, no auth)
"""

import logging
import time
from datetime import datetime, timedelta
from pathlib import Path

import numpy as np
import pandas as pd
import requests

from collectors.weather.stations import STATIONS, Station

logger = logging.getLogger(__name__)

# NOAA NOMADS base URL for GEFS
NOMADS_GEFS_BASE = "https://nomads.ncep.noaa.gov/cgi-bin/filter_gefs_atmos_0p25s.pl"

# AWS S3 base for GEFS v12 reforecast (2000-2019)
GEFS_REFORECAST_BASE = "https://noaa-gefs-retrospective.s3.amazonaws.com/GEFSv12/reforecast"

# Variables to extract
VARIABLES = {
    "TMP_2m": {"var": "TMP", "level": "2 m above ground"},  # 2m temperature
    "TMAX_2m": {"var": "TMAX", "level": "2 m above ground"},  # daily max temp
    "TMIN_2m": {"var": "TMIN", "level": "2 m above ground"},  # daily min temp
    "APCP_sfc": {"var": "APCP", "level": "surface"},  # accumulated precip
}

# Forecast lead times in hours (matching typical Kalshi contract horizons)
LEAD_TIMES_HOURS = [24, 48, 72, 96, 120, 144, 168]

# Number of ensemble members (control + 30 perturbations)
N_ENSEMBLE_MEMBERS = 31


def _kelvin_to_fahrenheit(k: float) -> float:
    return (k - 273.15) * 9 / 5 + 32


def _mm_to_inches(mm: float) -> float:
    return mm / 25.4


def fetch_gefs_reforecast_point(
    date: datetime,
    station: Station,
    lead_hours: list[int] | None = None,
    session: requests.Session | None = None,
) -> dict | None:
    """
    Fetch GEFS reforecast data for a single date and station.

    Uses the NOMADS GrADS Data Server (GDS) filter endpoint to extract
    a single grid point without downloading the full GRIB2 file.

    Returns dict with ensemble member values for each variable and lead time,
    or None if the request fails.
    """
    if lead_hours is None:
        lead_hours = LEAD_TIMES_HOURS
    if session is None:
        session = requests.Session()

    cycle = "00"  # Use 00Z cycle for consistency
    date_str = date.strftime("%Y%m%d")

    result = {
        "date": date.strftime("%Y-%m-%d"),
        "station_icao": station.icao,
        "lat": station.lat,
        "lon": station.lon,
        "cycle": cycle,
    }

    for lead_h in lead_hours:
        for var_name, var_info in VARIABLES.items():
            member_values = []

            for member_idx in range(N_ENSEMBLE_MEMBERS):
                if member_idx == 0:
                    member_id = "gec00"  # control
                else:
                    member_id = f"gep{member_idx:02d}"

                # NOMADS filter endpoint for point extraction
                # NOMADS CGI uses checkbox-style params: var_TMP=on, lev_2_m_above_ground=on
                lev_key = var_info["level"].replace(" ", "_")
                params = {
                    "file": f"{member_id}.t{cycle}z.pgrb2s.0p25.f{lead_h:03d}",
                    f"var_{var_info['var']}": "on",
                    f"lev_{lev_key}": "on",
                    "subregion": "",
                    "toplat": station.lat + 0.125,
                    "leftlon": (station.lon % 360) - 0.125,
                    "rightlon": (station.lon % 360) + 0.125,
                    "bottomlat": station.lat - 0.125,
                    "dir": f"/gefs.{date_str}/{cycle}/atmos/pgrb2sp25",
                }

                try:
                    resp = session.get(NOMADS_GEFS_BASE, params=params, timeout=30)
                    if resp.status_code == 200 and len(resp.content) > 100:
                        # Parse the single-point GRIB2 response
                        value = _parse_grib2_point(resp.content, var_info["var"])
                        if value is not None:
                            member_values.append(value)
                    else:
                        logger.debug(
                            "No data for %s member %d lead %dh on %s",
                            var_name, member_idx, lead_h, date_str,
                        )
                except requests.RequestException as e:
                    logger.warning("Request failed for %s: %s", member_id, e)
                    continue

                # Rate limit: be respectful to NOAA servers
                time.sleep(0.1)

            if member_values:
                key = f"{var_name}_f{lead_h:03d}"
                arr = np.array(member_values)

                # Convert units
                if "TMP" in var_name or "TMAX" in var_name or "TMIN" in var_name:
                    arr = np.array([_kelvin_to_fahrenheit(v) for v in arr])
                elif "APCP" in var_name:
                    arr = np.array([_mm_to_inches(v) for v in arr])

                result[f"{key}_members"] = arr.tolist()
                result[f"{key}_mean"] = float(np.mean(arr))
                result[f"{key}_std"] = float(np.std(arr))
                result[f"{key}_p10"] = float(np.percentile(arr, 10))
                result[f"{key}_p50"] = float(np.percentile(arr, 50))
                result[f"{key}_p90"] = float(np.percentile(arr, 90))
                result[f"{key}_count"] = len(member_values)

    return result


def _parse_grib2_point(data: bytes, variable: str) -> float | None:
    """
    Parse a single-point GRIB2 response from NOMADS filter.
    Falls back to cfgrib if eccodes direct parsing fails.
    """
    try:
        import eccodes

        msgid = eccodes.codes_new_from_message(data)
        try:
            values = eccodes.codes_get_values(msgid)
            if len(values) > 0:
                return float(values[0])
        finally:
            eccodes.codes_release(msgid)
    except Exception:
        pass

    # Fallback: write to temp file and use cfgrib
    try:
        import tempfile
        import xarray as xr

        with tempfile.NamedTemporaryFile(suffix=".grib2", delete=True) as f:
            f.write(data)
            f.flush()
            ds = xr.open_dataset(f.name, engine="cfgrib")
            # Get the first (and only) data variable
            for var in ds.data_vars:
                val = float(ds[var].values.flat[0])
                ds.close()
                return val
            ds.close()
    except Exception as e:
        logger.debug("cfgrib fallback failed: %s", e)

    return None


def fetch_reforecast_range(
    start_date: datetime,
    end_date: datetime,
    stations: list[Station] | None = None,
    output_dir: str = "data/backtest/weather",
    resume: bool = True,
) -> pd.DataFrame:
    """
    Download GFS ensemble reforecast data for a date range and set of stations.

    This is the main entry point for bulk historical data download.
    Downloads are rate-limited and can be resumed if interrupted.

    Args:
        start_date: First date to fetch (inclusive)
        end_date: Last date to fetch (inclusive)
        stations: List of stations to fetch. Defaults to all STATIONS.
        output_dir: Directory for output Parquet files
        resume: If True, skip dates that already exist in partial output

    Returns:
        DataFrame with all fetched data
    """
    if stations is None:
        stations = STATIONS

    output_path = Path(output_dir) / "gfs_reforecast.parquet"
    output_path.parent.mkdir(parents=True, exist_ok=True)

    # Load existing data for resume
    existing_records = []
    if resume and output_path.exists():
        existing_df = pd.read_parquet(output_path)
        existing_records = list(existing_df.to_dict("records"))
        existing_keys = {
            (r["date"], r["station_icao"]) for r in existing_records
        }
        logger.info("Resuming: %d existing records found", len(existing_records))
    else:
        existing_keys = set()

    session = requests.Session()
    session.headers.update({"User-Agent": "autonomy-platform/1.0 (weather research)"})

    records = list(existing_records)
    current = start_date
    total_days = (end_date - start_date).days + 1
    day_count = 0

    while current <= end_date:
        day_count += 1
        for station in stations:
            key = (current.strftime("%Y-%m-%d"), station.icao)
            if key in existing_keys:
                logger.debug("Skipping %s %s (already fetched)", key[0], key[1])
                continue

            logger.info(
                "[%d/%d] Fetching GFS reforecast for %s on %s",
                day_count, total_days, station.icao, current.strftime("%Y-%m-%d"),
            )

            record = fetch_gefs_reforecast_point(current, station, session=session)
            if record:
                records.append(record)

            # Respectful rate limiting between stations
            time.sleep(0.5)

        # Checkpoint: save every 7 days
        if day_count % 7 == 0 and records:
            df = pd.DataFrame(records)
            df.to_parquet(output_path, index=False)
            logger.info("Checkpoint saved: %d records", len(records))

        current += timedelta(days=1)

    # Final save
    if records:
        df = pd.DataFrame(records)
        df.to_parquet(output_path, index=False)
        logger.info("Final save: %d records to %s", len(records), output_path)
        return df

    return pd.DataFrame()


def main():
    """CLI entry point for downloading GFS reforecast data."""
    import argparse

    parser = argparse.ArgumentParser(description="Download GFS reforecast data")
    parser.add_argument("--start", required=True, help="Start date (YYYY-MM-DD)")
    parser.add_argument("--end", required=True, help="End date (YYYY-MM-DD)")
    parser.add_argument("--output-dir", default="data/backtest/weather")
    parser.add_argument("--no-resume", action="store_true")
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()

    logging.basicConfig(level=getattr(logging, args.log_level))

    start = datetime.strptime(args.start, "%Y-%m-%d")
    end = datetime.strptime(args.end, "%Y-%m-%d")

    df = fetch_reforecast_range(
        start_date=start,
        end_date=end,
        output_dir=args.output_dir,
        resume=not args.no_resume,
    )
    print(f"Downloaded {len(df)} records")
    if not df.empty:
        print(f"Date range: {df['date'].min()} to {df['date'].max()}")
        print(f"Stations: {df['station_icao'].nunique()}")


if __name__ == "__main__":
    main()
