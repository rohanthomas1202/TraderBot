"""
Open-Meteo Historical Forecast Data Fetcher

Downloads archived GFS and ECMWF deterministic forecasts from Open-Meteo's
Historical Forecast API for 2022-2025. This fills the gap left by NOMADS
(only ~4 days of operational GEFS data) and AWS reforecast (only 2000-2020).

Data source: Open-Meteo Historical Forecast API
- URL: https://historical-forecast-api.open-meteo.com/v1/forecast
- Free, no auth required
- Models: gfs_seamless (GFS deterministic), ecmwf_ifs (ECMWF IFS deterministic)
- Daily resolution with tmax, tmin, precipitation

Limitation: This provides deterministic forecasts, not ensemble members.
Historical GFS ensemble spread is not available for 2022-2025.
The EMOS model must use constant_sigma=True when trained on this data.

Output:
- gfs_reforecast.parquet: columns match existing schema (TMAX_2m_f024_mean, etc.)
  with _std columns set to 0.0 (no ensemble spread available)
- ecmwf_historical.parquet: columns match existing schema (ecmwf_tmax_f, etc.)
"""

import logging
import time
from datetime import datetime
from pathlib import Path

import pandas as pd
import requests

from collectors.weather.stations import STATIONS, Station

logger = logging.getLogger(__name__)

HISTORICAL_FORECAST_URL = "https://historical-forecast-api.open-meteo.com/v1/forecast"

# Open-Meteo caps request size; chunk into ~3-month windows
CHUNK_DAYS = 90


def fetch_station_forecasts(
    station: Station,
    start_date: datetime,
    end_date: datetime,
    session: requests.Session | None = None,
) -> tuple[pd.DataFrame | None, pd.DataFrame | None]:
    """
    Fetch GFS + ECMWF daily forecasts for a single station over a date range.

    Returns (gfs_df, ecmwf_df) or (None, None) on failure.
    """
    if session is None:
        session = requests.Session()

    params = {
        "latitude": station.lat,
        "longitude": station.lon,
        "start_date": start_date.strftime("%Y-%m-%d"),
        "end_date": end_date.strftime("%Y-%m-%d"),
        "daily": "temperature_2m_max,temperature_2m_min,precipitation_sum",
        "temperature_unit": "fahrenheit",
        "precipitation_unit": "inch",
        "timezone": "UTC",
        "models": "gfs_seamless,ecmwf_ifs",
    }

    try:
        resp = session.get(HISTORICAL_FORECAST_URL, params=params, timeout=60)
        resp.raise_for_status()
    except requests.RequestException as e:
        logger.error("Failed to fetch %s (%s to %s): %s",
                     station.icao, start_date.date(), end_date.date(), e)
        return None, None

    data = resp.json()
    daily = data.get("daily", {})
    dates = daily.get("time", [])

    if not dates:
        logger.warning("No data returned for %s", station.icao)
        return None, None

    # Build GFS DataFrame matching gfs_reforecast.parquet schema
    gfs_rows = []
    for i, d in enumerate(dates):
        tmax = daily.get("temperature_2m_max_gfs_seamless", daily.get("temperature_2m_max", []))[i]
        tmin = daily.get("temperature_2m_min_gfs_seamless", daily.get("temperature_2m_min", []))[i]
        precip = daily.get("precipitation_sum_gfs_seamless", daily.get("precipitation_sum", []))[i]

        if tmax is None and tmin is None:
            continue

        gfs_rows.append({
            "date": d,
            "station_icao": station.icao,
            "lat": station.lat,
            "lon": station.lon,
            "cycle": "00",
            "TMAX_2m_f024_mean": tmax,
            "TMAX_2m_f024_std": 0.0,
            "TMIN_2m_f024_mean": tmin,
            "TMIN_2m_f024_std": 0.0,
            "APCP_sfc_f024_mean": precip,
            "APCP_sfc_f024_std": 0.0,
            "data_source": "openmeteo_gfs_seamless",
        })

    # Build ECMWF DataFrame matching ecmwf_historical.parquet schema
    ecmwf_rows = []
    for i, d in enumerate(dates):
        tmax = daily.get("temperature_2m_max_ecmwf_ifs", [None])[i]
        tmin = daily.get("temperature_2m_min_ecmwf_ifs", [None])[i]
        precip = daily.get("precipitation_sum_ecmwf_ifs", [None])[i]

        if tmax is None and tmin is None:
            continue

        ecmwf_rows.append({
            "date": d,
            "station_icao": station.icao,
            "lat": station.lat,
            "lon": station.lon,
            "ecmwf_tmax_f": tmax,
            "ecmwf_tmin_f": tmin,
            "ecmwf_precip_in": precip,
            "data_source": "openmeteo_ecmwf_ifs",
        })

    gfs_df = pd.DataFrame(gfs_rows) if gfs_rows else None
    ecmwf_df = pd.DataFrame(ecmwf_rows) if ecmwf_rows else None

    if gfs_df is not None:
        logger.info("Fetched %d GFS + %d ECMWF rows for %s (%s to %s)",
                     len(gfs_df), len(ecmwf_df) if ecmwf_df is not None else 0,
                     station.icao, start_date.date(), end_date.date())

    return gfs_df, ecmwf_df


def fetch_all_stations(
    start_date: datetime,
    end_date: datetime,
    stations: list[Station] | None = None,
    output_dir: str = "data/backtest/weather",
) -> tuple[pd.DataFrame, pd.DataFrame]:
    """
    Download GFS + ECMWF forecast history for all stations.

    Chunks requests into CHUNK_DAYS windows to stay within API limits.
    Saves gfs_reforecast.parquet and ecmwf_historical.parquet.
    """
    if stations is None:
        stations = STATIONS

    output_path = Path(output_dir)
    output_path.mkdir(parents=True, exist_ok=True)

    session = requests.Session()
    session.headers.update({"User-Agent": "autonomy-platform/1.0 (weather research)"})

    all_gfs = []
    all_ecmwf = []

    from datetime import timedelta

    for station in stations:
        logger.info("Fetching forecasts for %s (%s)", station.icao, station.city)

        chunk_start = start_date
        while chunk_start < end_date:
            chunk_end = min(chunk_start + timedelta(days=CHUNK_DAYS), end_date)

            gfs_df, ecmwf_df = fetch_station_forecasts(
                station, chunk_start, chunk_end, session
            )

            if gfs_df is not None and not gfs_df.empty:
                all_gfs.append(gfs_df)
            if ecmwf_df is not None and not ecmwf_df.empty:
                all_ecmwf.append(ecmwf_df)

            chunk_start = chunk_end + timedelta(days=1)

            # Rate limit: Open-Meteo is free; be respectful
            time.sleep(0.5)

    # Concatenate and save GFS
    if all_gfs:
        gfs_result = pd.concat(all_gfs, ignore_index=True)
        gfs_result = gfs_result.sort_values(["station_icao", "date"]).reset_index(drop=True)
        gfs_path = output_path / "gfs_reforecast.parquet"
        gfs_result.to_parquet(gfs_path, index=False)
        logger.info("Saved %d GFS rows to %s", len(gfs_result), gfs_path)
    else:
        gfs_result = pd.DataFrame()
        logger.warning("No GFS data fetched")

    # Concatenate and save ECMWF
    if all_ecmwf:
        ecmwf_result = pd.concat(all_ecmwf, ignore_index=True)
        ecmwf_result = ecmwf_result.sort_values(["station_icao", "date"]).reset_index(drop=True)
        ecmwf_path = output_path / "ecmwf_historical.parquet"
        ecmwf_result.to_parquet(ecmwf_path, index=False)
        logger.info("Saved %d ECMWF rows to %s", len(ecmwf_result), ecmwf_path)
    else:
        ecmwf_result = pd.DataFrame()
        logger.warning("No ECMWF data fetched")

    return gfs_result, ecmwf_result


def main():
    """CLI entry point for downloading Open-Meteo historical forecast data."""
    import argparse

    parser = argparse.ArgumentParser(
        description="Download GFS + ECMWF historical forecasts from Open-Meteo"
    )
    parser.add_argument("--start", default="2022-01-01", help="Start date (YYYY-MM-DD)")
    parser.add_argument("--end", default="2025-12-31", help="End date (YYYY-MM-DD)")
    parser.add_argument("--output-dir", default="data/backtest/weather")
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()

    logging.basicConfig(
        level=getattr(logging, args.log_level),
        format="%(asctime)s %(name)s %(levelname)s %(message)s",
    )

    start = datetime.strptime(args.start, "%Y-%m-%d")
    end = datetime.strptime(args.end, "%Y-%m-%d")

    gfs_df, ecmwf_df = fetch_all_stations(start, end, output_dir=args.output_dir)

    print(f"GFS:   {len(gfs_df)} rows")
    print(f"ECMWF: {len(ecmwf_df)} rows")
    if not gfs_df.empty:
        print(f"Date range: {gfs_df['date'].min()} to {gfs_df['date'].max()}")
        print(f"Stations: {gfs_df['station_icao'].nunique()}")
        print(f"\nPer-station counts:")
        print(gfs_df.groupby("station_icao").size().to_string())


if __name__ == "__main__":
    main()
