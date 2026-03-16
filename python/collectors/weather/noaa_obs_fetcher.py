"""
NOAA ISD (Integrated Surface Database) Observations Fetcher

Downloads historical weather observations from NOAA's ISD dataset.
These are the ground-truth values used to train and evaluate the EMOS model.

Data source: NOAA ISD Lite
- URL: https://www.ncei.noaa.gov/data/global-hourly/access/
- Also available in ISD-Lite (simplified) format
- Free, no auth required
- Hourly observations from major airport weather stations
"""

import csv
import io
import logging
import time
from datetime import datetime, timedelta
from pathlib import Path

import numpy as np
import pandas as pd
import requests

from collectors.weather.stations import STATIONS, Station

logger = logging.getLogger(__name__)

# NOAA ISD-Lite base URL (simpler format, easier to parse)
ISD_LITE_BASE = "https://www.ncei.noaa.gov/data/global-hourly/access"

# NOAA LCD (Local Climatological Data) — better for daily summary stats
LCD_BASE = "https://www.ncei.noaa.gov/access/services/data/v1"


def fetch_lcd_observations(
    station: Station,
    start_date: datetime,
    end_date: datetime,
    session: requests.Session | None = None,
) -> pd.DataFrame | None:
    """
    Fetch Local Climatological Data (LCD) from NOAA for a station.

    LCD provides daily summary statistics including max/min temperature
    and precipitation totals, which directly map to Kalshi contract
    resolution criteria.

    Returns DataFrame with daily observations or None on failure.
    """
    if session is None:
        session = requests.Session()

    params = {
        "dataset": "local-climatological-data",
        "stations": station.isd_station_id,
        "startDate": start_date.strftime("%Y-%m-%dT00:00:00"),
        "endDate": end_date.strftime("%Y-%m-%dT23:59:59"),
        "format": "csv",
        "dataTypes": "DailyMaximumDryBulbTemperature,DailyMinimumDryBulbTemperature,"
                     "DailyPrecipitation,DailyAverageDryBulbTemperature,"
                     "DailyDepartureFromNormalAverageTemperature",
        "units": "standard",  # Fahrenheit for temp, inches for precip
    }

    try:
        resp = session.get(LCD_BASE, params=params, timeout=60)
        resp.raise_for_status()

        if not resp.text.strip():
            logger.warning("Empty response for station %s", station.icao)
            return None

        df = pd.read_csv(io.StringIO(resp.text), low_memory=False)

        if df.empty:
            logger.warning("No data returned for station %s", station.icao)
            return None

        # Normalize column names
        col_map = {
            "DATE": "date",
            "STATION": "station_id",
            "DailyMaximumDryBulbTemperature": "tmax_f",
            "DailyMinimumDryBulbTemperature": "tmin_f",
            "DailyPrecipitation": "precip_in",
            "DailyAverageDryBulbTemperature": "tavg_f",
            "DailyDepartureFromNormalAverageTemperature": "temp_departure_f",
        }

        available_cols = {k: v for k, v in col_map.items() if k in df.columns}
        df = df[list(available_cols.keys())].rename(columns=available_cols)

        # Parse date
        df["date"] = pd.to_datetime(df["date"]).dt.date.astype(str)

        # Convert numeric columns, handling "T" (trace) and "s" (suspect) flags
        for col in ["tmax_f", "tmin_f", "precip_in", "tavg_f", "temp_departure_f"]:
            if col in df.columns:
                df[col] = pd.to_numeric(
                    df[col].astype(str).str.replace(r"[sTM*]", "", regex=True),
                    errors="coerce",
                )

        # Add station metadata
        df["station_icao"] = station.icao
        df["lat"] = station.lat
        df["lon"] = station.lon

        # Drop rows where all weather values are NaN
        weather_cols = [c for c in ["tmax_f", "tmin_f", "precip_in"] if c in df.columns]
        df = df.dropna(subset=weather_cols, how="all")

        # Keep one row per date (LCD can have multiple reports per day)
        if "date" in df.columns:
            df = df.groupby("date").first().reset_index()

        logger.info(
            "Fetched %d daily observations for %s (%s to %s)",
            len(df), station.icao, start_date.date(), end_date.date(),
        )
        return df

    except requests.RequestException as e:
        logger.error("Failed to fetch LCD data for %s: %s", station.icao, e)
        return None


def fetch_observations_range(
    start_date: datetime,
    end_date: datetime,
    stations: list[Station] | None = None,
    output_dir: str = "data/backtest/weather",
    chunk_days: int = 365,
) -> pd.DataFrame:
    """
    Download NOAA observations for all stations over a date range.

    Downloads in yearly chunks to stay within API limits.
    Saves to Parquet with checkpointing.
    """
    if stations is None:
        stations = STATIONS

    output_path = Path(output_dir) / "noaa_observations.parquet"
    output_path.parent.mkdir(parents=True, exist_ok=True)

    session = requests.Session()
    session.headers.update({"User-Agent": "autonomy-platform/1.0 (weather research)"})

    all_frames = []

    for station in stations:
        logger.info("Fetching observations for %s (%s)", station.icao, station.city)

        # Download in chunks to respect API limits
        chunk_start = start_date
        while chunk_start < end_date:
            chunk_end = min(chunk_start + timedelta(days=chunk_days), end_date)

            df = fetch_lcd_observations(station, chunk_start, chunk_end, session)
            if df is not None and not df.empty:
                all_frames.append(df)

            chunk_start = chunk_end + timedelta(days=1)

            # Rate limit between API calls
            time.sleep(1.0)

    if all_frames:
        result = pd.concat(all_frames, ignore_index=True)
        result = result.sort_values(["station_icao", "date"]).reset_index(drop=True)
        result.to_parquet(output_path, index=False)
        logger.info("Saved %d observations to %s", len(result), output_path)
        return result

    logger.warning("No observations fetched")
    return pd.DataFrame()


def main():
    """CLI entry point for downloading NOAA observations."""
    import argparse

    parser = argparse.ArgumentParser(description="Download NOAA station observations")
    parser.add_argument("--start", required=True, help="Start date (YYYY-MM-DD)")
    parser.add_argument("--end", required=True, help="End date (YYYY-MM-DD)")
    parser.add_argument("--output-dir", default="data/backtest/weather")
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()

    logging.basicConfig(level=getattr(logging, args.log_level))

    start = datetime.strptime(args.start, "%Y-%m-%d")
    end = datetime.strptime(args.end, "%Y-%m-%d")

    df = fetch_observations_range(start, end, output_dir=args.output_dir)
    print(f"Downloaded {len(df)} observation records")
    if not df.empty:
        print(f"Date range: {df['date'].min()} to {df['date'].max()}")
        print(f"Stations: {df['station_icao'].nunique()}")
        print(f"\nPer-station counts:")
        print(df.groupby("station_icao").size().to_string())


if __name__ == "__main__":
    main()
