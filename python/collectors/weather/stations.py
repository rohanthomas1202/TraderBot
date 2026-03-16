"""
Target weather stations mapped to Kalshi contract cities.
Each entry maps a Kalshi location to its nearest NOAA ISD station
and the corresponding GFS/ECMWF grid point.
"""

from dataclasses import dataclass


@dataclass(frozen=True)
class Station:
    kalshi_location: str  # Kalshi contract location identifier
    city: str
    isd_station_id: str  # NOAA ISD station ID (USAF-WBAN)
    icao: str  # ICAO airport code
    lat: float
    lon: float


# Primary stations for Kalshi weather contract cities
STATIONS = [
    Station("NYC", "New York", "725053-94728", "KJFK", 40.639, -73.762),
    Station("NYC", "New York (LGA)", "725030-14732", "KLGA", 40.779, -73.880),
    Station("CHI", "Chicago", "725300-94846", "KORD", 41.985, -87.907),
    Station("LAX", "Los Angeles", "722950-23174", "KLAX", 33.938, -118.389),
    Station("MIA", "Miami", "722020-12839", "KMIA", 25.791, -80.316),
    Station("DFW", "Dallas-Fort Worth", "722590-03927", "KDFW", 32.899, -97.040),
    Station("DEN", "Denver", "725650-03017", "KDEN", 39.833, -104.658),
    Station("ATL", "Atlanta", "722190-13874", "KATL", 33.640, -84.427),
    Station("PHX", "Phoenix", "722780-23183", "KPHX", 33.428, -112.004),
    Station("SEA", "Seattle", "727930-24233", "KSEA", 47.449, -122.314),
]

# Map ICAO codes to Station objects for quick lookup
STATION_BY_ICAO = {s.icao: s for s in STATIONS}

# Unique lat/lon pairs for GFS grid point extraction
GFS_GRID_POINTS = list({(s.lat, s.lon) for s in STATIONS})
