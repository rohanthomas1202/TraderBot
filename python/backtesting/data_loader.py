"""
Data loader for backtesting.

Loads historical weather data from Parquet files and aligns GFS forecasts
with NOAA observations and ECMWF data for model training and evaluation.

The loader produces aligned (features, observations) pairs that can be
fed directly to the EMOS model's train() and evaluate() methods.
"""

import logging
from datetime import datetime
from pathlib import Path

import numpy as np
import pandas as pd

logger = logging.getLogger(__name__)

DEFAULT_DATA_DIR = "data/backtest/weather"


class WeatherDataLoader:
    """
    Loads and aligns weather data for backtesting.

    Produces training/test splits with features (GFS + ECMWF ensemble stats)
    aligned to ground-truth observations (NOAA station data).
    """

    def __init__(self, data_dir: str = DEFAULT_DATA_DIR):
        self.data_dir = Path(data_dir)
        self._gfs: pd.DataFrame | None = None
        self._ecmwf: pd.DataFrame | None = None
        self._obs: pd.DataFrame | None = None

    def load(self) -> "WeatherDataLoader":
        """Load all Parquet files into memory."""
        gfs_path = self.data_dir / "gfs_reforecast.parquet"
        ecmwf_path = self.data_dir / "ecmwf_historical.parquet"
        obs_path = self.data_dir / "noaa_observations.parquet"

        if gfs_path.exists():
            self._gfs = pd.read_parquet(gfs_path)
            logger.info("Loaded GFS data: %d rows", len(self._gfs))
        else:
            logger.warning("GFS data not found at %s", gfs_path)

        if ecmwf_path.exists():
            self._ecmwf = pd.read_parquet(ecmwf_path)
            logger.info("Loaded ECMWF data: %d rows", len(self._ecmwf))
        else:
            logger.warning("ECMWF data not found at %s", ecmwf_path)

        if obs_path.exists():
            self._obs = pd.read_parquet(obs_path)
            logger.info("Loaded NOAA observations: %d rows", len(self._obs))
        else:
            logger.warning("NOAA observations not found at %s", obs_path)

        return self

    def get_aligned_data(
        self,
        start_date: str,
        end_date: str,
        variable: str = "tmax",
        lead_hours: int = 24,
        stations: list[str] | None = None,
    ) -> dict:
        """
        Produce aligned (forecast, observation) arrays for a date range.

        Args:
            start_date: Start date (YYYY-MM-DD), inclusive.
            end_date: End date (YYYY-MM-DD), inclusive.
            variable: Weather variable ('tmax', 'tmin', 'precip').
            lead_hours: Forecast lead time in hours.
            stations: List of ICAO station codes. None = all stations.

        Returns:
            Dict with arrays suitable for EMOSModel.train() / evaluate():
            - gfs_mean, gfs_std: GFS ensemble statistics
            - ecmwf_mean: ECMWF ERA5 value
            - observations: NOAA ground truth
            - dates, station_icaos: Metadata arrays
        """
        if self._gfs is None or self._obs is None:
            raise RuntimeError("Data not loaded. Call load() first.")

        # Map variable name to column names
        gfs_var_map = {
            "tmax": f"TMAX_2m_f{lead_hours:03d}",
            "tmin": f"TMIN_2m_f{lead_hours:03d}",
            "precip": f"APCP_sfc_f{lead_hours:03d}",
            "temp": f"TMP_2m_f{lead_hours:03d}",
        }
        obs_var_map = {
            "tmax": "tmax_f",
            "tmin": "tmin_f",
            "precip": "precip_in",
            "temp": "tavg_f",
        }
        ecmwf_var_map = {
            "tmax": "ecmwf_tmax_f",
            "tmin": "ecmwf_tmin_f",
            "precip": "ecmwf_precip_in",
            "temp": "ecmwf_tmean_f",
        }

        gfs_prefix = gfs_var_map.get(variable)
        obs_col = obs_var_map.get(variable)
        ecmwf_col = ecmwf_var_map.get(variable)

        if not gfs_prefix or not obs_col:
            raise ValueError(f"Unknown variable: {variable}")

        # Filter by date range
        gfs = self._gfs[
            (self._gfs["date"] >= start_date) & (self._gfs["date"] <= end_date)
        ].copy()
        obs = self._obs[
            (self._obs["date"] >= start_date) & (self._obs["date"] <= end_date)
        ].copy()

        if stations:
            gfs = gfs[gfs["station_icao"].isin(stations)]
            obs = obs[obs["station_icao"].isin(stations)]

        # Check which GFS columns are available
        gfs_mean_col = f"{gfs_prefix}_mean"
        gfs_std_col = f"{gfs_prefix}_std"

        if gfs_mean_col not in gfs.columns:
            logger.warning("GFS column %s not found. Available: %s", gfs_mean_col, list(gfs.columns))
            return self._empty_result()

        # Merge GFS + observations on (date, station)
        merged = pd.merge(
            gfs[["date", "station_icao", gfs_mean_col, gfs_std_col]].dropna(),
            obs[["date", "station_icao", obs_col]].dropna(),
            on=["date", "station_icao"],
            how="inner",
        )

        # Merge ECMWF if available
        if self._ecmwf is not None and ecmwf_col:
            ecmwf = self._ecmwf[
                (self._ecmwf["date"] >= start_date) & (self._ecmwf["date"] <= end_date)
            ].copy()
            if stations:
                ecmwf = ecmwf[ecmwf["station_icao"].isin(stations)]
            if ecmwf_col in ecmwf.columns:
                merged = pd.merge(
                    merged,
                    ecmwf[["date", "station_icao", ecmwf_col]].dropna(),
                    on=["date", "station_icao"],
                    how="left",
                )

        if merged.empty:
            logger.warning("No aligned data found for %s to %s", start_date, end_date)
            return self._empty_result()

        # Build result arrays
        result = {
            "gfs_mean": merged[gfs_mean_col].values,
            "gfs_std": merged[gfs_std_col].values,
            "observations": merged[obs_col].values,
            "dates": merged["date"].values,
            "station_icaos": merged["station_icao"].values,
        }

        # ECMWF mean (fall back to GFS mean if not available)
        if ecmwf_col and ecmwf_col in merged.columns:
            ecmwf_values = merged[ecmwf_col].values
            # Fill NaN with GFS mean
            nan_mask = np.isnan(ecmwf_values)
            ecmwf_values[nan_mask] = result["gfs_mean"][nan_mask]
            result["ecmwf_mean"] = ecmwf_values
        else:
            result["ecmwf_mean"] = result["gfs_mean"].copy()

        logger.info(
            "Aligned data: %d samples, %s to %s, %d stations",
            len(merged), start_date, end_date,
            merged["station_icao"].nunique(),
        )
        return result

    def add_threshold_outcomes(
        self,
        data: dict,
        thresholds: list[float] | None = None,
    ) -> dict:
        """
        Add binary threshold exceedance outcomes to aligned data.

        For each (observation, threshold) pair, computes whether
        the observation exceeded the threshold. This is what Kalshi
        weather contracts resolve on.

        If no thresholds are provided, generates common temperature
        thresholds around the observed values.
        """
        observations = data["observations"]

        if thresholds is None:
            # Generate thresholds around the data distribution
            p25, p50, p75 = np.percentile(observations, [25, 50, 75])
            thresholds = [
                p25 - 5, p25, p50 - 5, p50, p50 + 5, p75, p75 + 5,
            ]
            thresholds = [round(t) for t in thresholds]

        # Expand data: each sample × each threshold
        n_samples = len(observations)
        n_thresholds = len(thresholds)
        n_total = n_samples * n_thresholds

        expanded = {
            "gfs_mean": np.repeat(data["gfs_mean"], n_thresholds),
            "gfs_std": np.repeat(data["gfs_std"], n_thresholds),
            "ecmwf_mean": np.repeat(data["ecmwf_mean"], n_thresholds),
            "observations": np.repeat(observations, n_thresholds),
            "dates": np.repeat(data["dates"], n_thresholds),
            "station_icaos": np.repeat(data["station_icaos"], n_thresholds),
            "thresholds": np.tile(thresholds, n_samples),
            "outcomes": np.zeros(n_total),
        }

        # Compute binary outcomes: 1 if observation > threshold
        expanded["outcomes"] = (
            expanded["observations"] > expanded["thresholds"]
        ).astype(np.float64)

        logger.info(
            "Added %d thresholds, expanded to %d samples (%.1f%% positive)",
            n_thresholds, n_total, 100 * expanded["outcomes"].mean(),
        )
        return expanded

    def _empty_result(self) -> dict:
        return {
            "gfs_mean": np.array([]),
            "gfs_std": np.array([]),
            "ecmwf_mean": np.array([]),
            "observations": np.array([]),
            "dates": np.array([]),
            "station_icaos": np.array([]),
        }
