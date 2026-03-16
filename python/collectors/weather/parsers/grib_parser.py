"""
GRIB2 file parser for NWP model output.

Handles parsing of GRIB2 files from NOAA NOMADS (GFS, GEFS)
using the eccodes or cfgrib libraries.

GRIB2 is the standard format for numerical weather prediction data.
It's a binary format that requires specialized libraries to read.
"""

import logging
import tempfile
from pathlib import Path

import numpy as np

logger = logging.getLogger(__name__)


def extract_point_from_grib2(
    data: bytes,
    lat: float,
    lon: float,
    variable: str | None = None,
) -> float | None:
    """
    Extract a single point value from a GRIB2 message.

    Args:
        data: Raw GRIB2 bytes.
        lat: Target latitude.
        lon: Target longitude (will be converted to 0-360 if negative).
        variable: Optional variable filter (e.g., 'TMP', 'APCP').

    Returns:
        Extracted value at the nearest grid point, or None on failure.
    """
    # Normalize longitude to 0-360 for GFS convention
    if lon < 0:
        lon = lon + 360

    # Try eccodes first (faster, more reliable)
    try:
        return _extract_with_eccodes(data, lat, lon, variable)
    except Exception as e:
        logger.debug("eccodes extraction failed: %s", e)

    # Fallback to cfgrib via xarray
    try:
        return _extract_with_cfgrib(data, lat, lon)
    except Exception as e:
        logger.debug("cfgrib extraction failed: %s", e)

    return None


def _extract_with_eccodes(
    data: bytes,
    lat: float,
    lon: float,
    variable: str | None = None,
) -> float:
    """Extract using the eccodes C library directly."""
    import eccodes

    msgid = eccodes.codes_new_from_message(data)
    try:
        # Verify variable if specified
        if variable:
            short_name = eccodes.codes_get(msgid, "shortName")
            if short_name != variable and variable not in short_name:
                raise ValueError(f"Variable mismatch: expected {variable}, got {short_name}")

        # Find nearest grid point
        nearest = eccodes.codes_grib_find_nearest(msgid, lat, lon, is_lsm=False, npoints=1)
        if nearest and len(nearest) > 0:
            return float(nearest[0].value)

        # Fallback: get all values and find nearest by index
        ni = eccodes.codes_get(msgid, "Ni")
        nj = eccodes.codes_get(msgid, "Nj")
        lat_first = eccodes.codes_get(msgid, "latitudeOfFirstGridPointInDegrees")
        lon_first = eccodes.codes_get(msgid, "longitudeOfFirstGridPointInDegrees")
        lat_last = eccodes.codes_get(msgid, "latitudeOfLastGridPointInDegrees")
        lon_last = eccodes.codes_get(msgid, "longitudeOfLastGridPointInDegrees")

        dlat = (lat_last - lat_first) / (nj - 1) if nj > 1 else 0
        dlon = (lon_last - lon_first) / (ni - 1) if ni > 1 else 0

        if dlat != 0 and dlon != 0:
            j = int(round((lat - lat_first) / dlat))
            i = int(round((lon - lon_first) / dlon))
            j = max(0, min(j, nj - 1))
            i = max(0, min(i, ni - 1))

            values = eccodes.codes_get_values(msgid)
            idx = j * ni + i
            if 0 <= idx < len(values):
                return float(values[idx])

        raise ValueError("Could not extract point value")
    finally:
        eccodes.codes_release(msgid)


def _extract_with_cfgrib(data: bytes, lat: float, lon: float) -> float:
    """Extract using cfgrib via xarray (writes to temp file)."""
    import xarray as xr

    with tempfile.NamedTemporaryFile(suffix=".grib2", delete=True) as f:
        f.write(data)
        f.flush()

        ds = xr.open_dataset(f.name, engine="cfgrib")
        try:
            # Select nearest point
            point = ds.sel(latitude=lat, longitude=lon, method="nearest")
            # Get the first data variable
            for var_name in ds.data_vars:
                val = float(point[var_name].values)
                return val
        finally:
            ds.close()

    raise ValueError("No data variables found in GRIB2 file")


def parse_grib2_file(
    filepath: str,
    lat: float,
    lon: float,
    variables: list[str] | None = None,
) -> dict[str, float]:
    """
    Parse a GRIB2 file and extract point values for specified variables.

    Args:
        filepath: Path to the GRIB2 file.
        lat: Target latitude.
        lon: Target longitude.
        variables: List of variable short names to extract. None = all.

    Returns:
        Dict mapping variable name to extracted value.
    """
    import eccodes

    results = {}
    path = Path(filepath)

    if not path.exists():
        raise FileNotFoundError(f"GRIB2 file not found: {filepath}")

    with open(filepath, "rb") as f:
        while True:
            msgid = eccodes.codes_grib_new_from_file(f)
            if msgid is None:
                break

            try:
                short_name = eccodes.codes_get(msgid, "shortName")
                level_type = eccodes.codes_get(msgid, "typeOfLevel")
                level = eccodes.codes_get(msgid, "level")

                if variables and short_name not in variables:
                    continue

                key = f"{short_name}_{level_type}_{level}"

                # Normalize longitude
                target_lon = lon + 360 if lon < 0 else lon

                nearest = eccodes.codes_grib_find_nearest(
                    msgid, lat, target_lon, is_lsm=False, npoints=1
                )
                if nearest and len(nearest) > 0:
                    results[key] = float(nearest[0].value)

            except Exception as e:
                logger.debug("Error parsing GRIB2 message: %s", e)
            finally:
                eccodes.codes_release(msgid)

    return results
