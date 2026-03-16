"""
Dynamic weather market discovery for Kalshi.

Instead of hardcoding ticker prefixes, this module discovers active daily
weather markets by querying the Kalshi series and events APIs. Markets are
classified into categories (daily_high, daily_low, rain, snow) by title
pattern matching.

Usage:
    from market_logger.discovery import WeatherMarketDiscovery

    discovery = WeatherMarketDiscovery(kalshi_client)
    active = discovery.discover()
    print(f"Found {active.total_markets} markets across {len(active.series)} series")
"""

import logging
import re
import time
from dataclasses import dataclass, field

logger = logging.getLogger(__name__)

# Title patterns used to classify series into weather categories.
# These are matched case-insensitively against the series title.
CATEGORY_PATTERNS: dict[str, list[str]] = {
    "daily_high": [
        r"daily\s+high\s+temp",
        r"high\s+temp.*daily",
        r"max(imum)?\s+temp.*daily",
        r"daily\s+max",
        r"highest\s+temp.*\bin\b",
        r"high\s+temp\s+in\b",
    ],
    "daily_low": [
        r"daily\s+low\s+temp",
        r"low(est)?\s+temp.*\bin\b",
        r"min(imum)?\s+temp.*daily",
        r"lowest\s+temp",
    ],
    "rain": [
        r"\brain\b",
        r"\brainfall\b",
        r"\bprecip",
    ],
    "snow": [
        r"\bsnow",
    ],
}


@dataclass
class DiscoveredSeries:
    """A Kalshi series with active daily weather events."""

    series_ticker: str
    title: str
    category: str  # "daily_high", "daily_low", "rain", "snow", "other_weather"
    active_event_count: int = 0
    active_market_count: int = 0
    sample_event_ticker: str = ""


@dataclass
class DiscoveryResult:
    """Result of a weather market discovery run."""

    series: list[DiscoveredSeries] = field(default_factory=list)
    total_markets: int = 0
    discovery_time: float = 0.0

    @property
    def by_category(self) -> dict[str, list[DiscoveredSeries]]:
        result: dict[str, list[DiscoveredSeries]] = {}
        for s in self.series:
            result.setdefault(s.category, []).append(s)
        return result


def _classify_series(title: str) -> str:
    """Classify a series title into a weather category."""
    lower = title.lower()
    for category, patterns in CATEGORY_PATTERNS.items():
        for pattern in patterns:
            if re.search(pattern, lower):
                return category
    return "other_weather"


class WeatherMarketDiscovery:
    """Discovers active daily weather markets on Kalshi dynamically."""

    WEATHER_CATEGORY = "Climate and Weather"

    def __init__(self, kalshi_client, rate_delay: float = 0.2):
        self.client = kalshi_client
        self.rate_delay = rate_delay

    def discover(self, include_other: bool = False) -> DiscoveryResult:
        """
        Discover all active daily weather series and their markets.

        Args:
            include_other: If True, include non-daily weather series that
                have active events (hurricanes, earthquakes, etc.)

        Returns:
            DiscoveryResult with all discovered active series.
        """
        start = time.time()

        # Step 1: Get all Climate and Weather series from the API
        all_weather_series = self._fetch_weather_series()
        logger.info("Found %d Climate and Weather series", len(all_weather_series))

        # Step 2: Classify each series
        classified = []
        for s in all_weather_series:
            category = _classify_series(s.get("title", ""))
            classified.append((s, category))

        # Step 3: Filter to daily weather types (+ optionally other)
        daily_categories = {"daily_high", "daily_low", "rain", "snow"}
        candidates = [
            (s, cat) for s, cat in classified
            if cat in daily_categories or (include_other and cat == "other_weather")
        ]
        logger.info(
            "Classified %d series as daily weather candidates",
            len(candidates),
        )

        # Step 4: Check which candidates have active events
        active_series = []
        total_markets = 0

        for s, category in candidates:
            series_ticker = s.get("ticker", "")
            time.sleep(self.rate_delay)

            events = self._fetch_open_events(series_ticker)
            if not events:
                continue

            # Count markets across events
            market_count = 0
            sample_event = events[0].get("event_ticker", "")

            for event in events:
                time.sleep(self.rate_delay)
                markets = self._fetch_event_markets(event.get("event_ticker", ""))
                market_count += len(markets)

            ds = DiscoveredSeries(
                series_ticker=series_ticker,
                title=s.get("title", ""),
                category=category,
                active_event_count=len(events),
                active_market_count=market_count,
                sample_event_ticker=sample_event,
            )
            active_series.append(ds)
            total_markets += market_count

            logger.debug(
                "Active: %s (%s) — %d events, %d markets",
                series_ticker, category, len(events), market_count,
            )

        elapsed = time.time() - start
        result = DiscoveryResult(
            series=active_series,
            total_markets=total_markets,
            discovery_time=elapsed,
        )

        logger.info(
            "Discovery complete in %.1fs: %d active series, %d total markets",
            elapsed, len(active_series), total_markets,
        )
        return result

    def get_active_market_tickers(self) -> list[dict]:
        """
        Return a flat list of all active weather market dicts.

        Each dict contains: market_id, ticker, title, event_ticker,
        series_ticker, category, yes_bid, yes_ask, volume, etc.
        """
        result = self.discover()
        all_markets = []

        for series in result.series:
            events = self._fetch_open_events(series.series_ticker)
            for event in events:
                time.sleep(self.rate_delay)
                markets = self._fetch_event_markets(event.get("event_ticker", ""))
                for m in markets:
                    m["_series_ticker"] = series.series_ticker
                    m["_series_category"] = series.category
                    all_markets.append(m)

        return all_markets

    def _fetch_weather_series(self) -> list[dict]:
        """Fetch all series in the Climate and Weather category."""
        all_series = []
        try:
            resp = self.client._authed_request(
                "GET", "/series", params={"limit": 200}
            )
            all_series = resp.json().get("series", [])
        except Exception as e:
            logger.error("Failed to fetch series: %s", e)
            return []

        return [
            s for s in all_series
            if s.get("category") == self.WEATHER_CATEGORY
        ]

    def _fetch_open_events(self, series_ticker: str) -> list[dict]:
        """Fetch open events for a given series."""
        try:
            resp = self.client._authed_request(
                "GET",
                "/events",
                params={
                    "series_ticker": series_ticker,
                    "status": "open",
                    "limit": 50,
                },
            )
            return resp.json().get("events", [])
        except Exception as e:
            logger.debug("Failed to fetch events for %s: %s", series_ticker, e)
            return []

    def _fetch_event_markets(self, event_ticker: str) -> list[dict]:
        """Fetch all markets for a given event."""
        try:
            resp = self.client._authed_request(
                "GET",
                "/markets",
                params={
                    "event_ticker": event_ticker,
                    "limit": 100,
                },
            )
            return resp.json().get("markets", [])
        except Exception as e:
            logger.debug("Failed to fetch markets for %s: %s", event_ticker, e)
            return []


def print_discovery_report(result: DiscoveryResult):
    """Print a human-readable discovery report."""
    print("=" * 70)
    print("KALSHI WEATHER MARKET DISCOVERY")
    print(f"Discovery time: {result.discovery_time:.1f}s")
    print(f"Active series: {len(result.series)}")
    print(f"Total markets: {result.total_markets}")
    print("=" * 70)
    print()

    for category, series_list in sorted(result.by_category.items()):
        total_m = sum(s.active_market_count for s in series_list)
        print(f"--- {category} ({len(series_list)} series, {total_m} markets) ---")
        for s in sorted(series_list, key=lambda x: x.series_ticker):
            print(
                f"  {s.series_ticker:30s} events={s.active_event_count:2d}  "
                f"markets={s.active_market_count:3d}  {s.title[:40]}"
            )
        print()

    print("=" * 70)
