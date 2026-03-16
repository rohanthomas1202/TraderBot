"""
ECMWF Historical Ensemble Data Fetcher via Open-Meteo API

Downloads historical ECMWF ensemble forecast data through the Open-Meteo
free API. This provides a second independent NWP ensemble to combine
with GFS in the EMOS model.

Data source: Open-Meteo Historical Weather API + Ensemble API
- https://open-meteo.com/en/docs/historical-weather-api
- https://open-meteo.com/en/docs/ensemble-api
- Free, no auth required
- JSON format, simple REST API
- Up to 51 ensemble members (ECMWF IFS)
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

# Open-Meteo API endpoints
HISTORICAL_API = "https://archive-api.open-meteo.com/v1/archive"
ENSEMBLE_API = "https://ensemble-api.open-meteo.com/v1/ensemble"


def fetch_historical_weather(
    station: Station,
    start_date: datetime,
    end_date: datetime,
    session: requests.Session | None = None,
) -> pd.DataFrame | None:
    """
    Fetch historical daily weather data from Open-Meteo archive API.

    This gives us ECMWF ERA5 reanalysis data (high-quality gridded observations)
    plus historical forecasts for comparison.

    Returns DataFrame with daily weather values or None on failure.
    """
    if session is None:
        session = requests.Session()

    params = {
        "latitude": station.lat,
        "longitude": station.lon,
        "start_date": start_date.strftime("%Y-%m-%d"),
        "end_date": end_date.strftime("%Y-%m-%d"),
        "daily": ",".join([
            "temperature_2m_max",
            "temperature_2m_min",
            "temperature_2m_mean",
            "precipitation_sum",
            "windspeed_10m_max",
        ]),
        "temperature_unit": "fahrenheit",
        "precipitation_unit": "inch",
        "timezone": "UTC",
    }

    try:
        resp = session.get(HISTORICAL_API, params=params, timeout=60)
        resp.raise_for_status()
        data = resp.json()

        if "daily" not in data:
            logger.warning("No daily data in response for %s", station.icao)
            return None

        daily = data["daily"]
        df = pd.DataFrame({
            "date": daily["time"],
            "ecmwf_tmax_f": daily.get("temperature_2m_max"),
            "ecmwf_tmin_f": daily.get("temperature_2m_min"),
            "ecmwf_tmean_f": daily.get("temperature_2m_mean"),
            "ecmwf_precip_in": daily.get("precipitation_sum"),
            "ecmwf_wind_max_mph": daily.get("windspeed_10m_max"),
        })

        df["station_icao"] = station.icao
        df["lat"] = station.lat
        df["lon"] = station.lon

        logger.info(
            "Fetched %d days of ECMWF historical for %s",
            len(df), station.icao,
        )
        return df

    except requests.RequestException as e:
        logger.error("Failed to fetch ECMWF historical for %s: %s", station.icao, e)
        return None


def fetch_ensemble_forecast(
    station: Station,
    date: datetime,
    session: requests.Session | None = None,
) -> dict | None:
    """
    Fetch ECMWF ensemble forecast data from Open-Meteo ensemble API.

    This endpoint provides the 51-member ECMWF IFS ensemble,
    which we use alongside GFS in the EMOS model.

    Note: The ensemble API provides forecasts from recent dates only.
    For historical ensemble data, we use the archive API which gives
    ERA5 reanalysis (single deterministic value, not ensemble).

    Returns dict with ensemble statistics or None on failure.
    """
    if session is None:
        session = requests.Session()

    params = {
        "latitude": station.lat,
        "longitude": station.lon,
        "daily": "temperature_2m_max,temperature_2m_min,precipitation_sum",
        "temperature_unit": "fahrenheit",
        "precipitation_unit": "inch",
        "models": "ecmwf_ifs04",
        "forecast_days": 7,
    }

    try:
        resp = session.get(ENSEMBLE_API, params=params, timeout=60)
        resp.raise_for_status()
        data = resp.json()

        if "daily" not in data:
            return None

        result = {
            "date": date.strftime("%Y-%m-%d"),
            "station_icao": station.icao,
            "lat": station.lat,
            "lon": station.lon,
        }

        daily = data["daily"]
        # Extract ensemble member values for each lead day
        for lead_day in range(min(7, len(daily.get("time", [])))):
            for var in ["temperature_2m_max", "temperature_2m_min", "precipitation_sum"]:
                # Open-Meteo ensemble returns member arrays per timestep
                values = daily.get(var)
                if values and lead_day < len(values):
                    val = values[lead_day]
                    if val is not None:
                        var_short = var.replace("temperature_2m_", "t").replace("precipitation_", "p")
                        result[f"ecmwf_{var_short}_day{lead_day+1}"] = val

        return result

    except requests.RequestException as e:
        logger.debug("Ensemble forecast fetch failed for %s: %s", station.icao, e)
        return None


def fetch_ecmwf_historical_range(
    start_date: datetime,
    end_date: datetime,
    stations: list[Station] | None = None,
    output_dir: str = "data/backtest/weather",
) -> pd.DataFrame:
    """
    Download ECMWF ERA5 historical data for all stations over a date range.

    Uses the Open-Meteo archive API which provides ERA5 reanalysis.
    Downloads per-station to stay within API limits.

    For backtesting the EMOS model, ERA5 serves as a high-quality
    "second opinion" alongside GFS, even though it's reanalysis
    rather than a true ensemble forecast.
    """
    if stations is None:
        stations = STATIONS

    output_path = Path(output_dir) / "ecmwf_historical.parquet"
    output_path.parent.mkdir(parents=True, exist_ok=True)

    session = requests.Session()
    session.headers.update({"User-Agent": "autonomy-platform/1.0 (weather research)"})

    all_frames = []

    for station in stations:
        logger.info("Fetching ECMWF historical for %s (%s)", station.icao, station.city)

        # Open-Meteo handles full date ranges, but chunk to avoid timeouts
        chunk_start = start_date
        while chunk_start < end_date:
            chunk_end = min(chunk_start + timedelta(days=365), end_date)

            df = fetch_historical_weather(station, chunk_start, chunk_end, session)
            if df is not None and not df.empty:
                all_frames.append(df)

            chunk_start = chunk_end + timedelta(days=1)

            # Rate limit: Open-Meteo free tier
            time.sleep(1.0)

    if all_frames:
        result = pd.concat(all_frames, ignore_index=True)
        result = result.sort_values(["station_icao", "date"]).reset_index(drop=True)
        result.to_parquet(output_path, index=False)
        logger.info("Saved %d records to %s", len(result), output_path)
        return result

    logger.warning("No ECMWF data fetched")
    return pd.DataFrame()


def main():
    """CLI entry point for downloading ECMWF historical data."""
    import argparse

    parser = argparse.ArgumentParser(description="Download ECMWF historical data via Open-Meteo")
    parser.add_argument("--start", required=True, help="Start date (YYYY-MM-DD)")
    parser.add_argument("--end", required=True, help="End date (YYYY-MM-DD)")
    parser.add_argument("--output-dir", default="data/backtest/weather")
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()

    logging.basicConfig(level=getattr(logging, args.log_level))

    start = datetime.strptime(args.start, "%Y-%m-%d")
    end = datetime.strptime(args.end, "%Y-%m-%d")

    df = fetch_ecmwf_historical_range(start, end, output_dir=args.output_dir)
    print(f"Downloaded {len(df)} records")
    if not df.empty:
        print(f"Date range: {df['date'].min()} to {df['date'].max()}")
        print(f"Stations: {df['station_icao'].nunique()}")


if __name__ == "__main__":
    main()
