"""
Kalshi Orderbook Logger

Periodically snapshots Kalshi weather market orderbooks and stores them
in Postgres for liquidity analysis. Runs as a long-lived background process
during Phase 1 to answer: "Is there enough liquidity to trade profitably?"

Kalshi API v2:
- Base URL: https://api.elections.kalshi.com/trade-api/v2
- Auth: RSA key-pair (KALSHI_API_KEY_ID + private key PEM file)
- Rate limit: 10 requests/second
- Markets endpoint: GET /markets (filterable by category)
- Orderbook endpoint: GET /markets/{ticker}/orderbook
"""

import base64
import logging
import os
import time
from datetime import datetime, timezone

import psycopg2
import psycopg2.extras
import requests
import schedule

logger = logging.getLogger(__name__)

KALSHI_API_BASE_DEFAULT = "https://api.elections.kalshi.com/trade-api/v2"
KALSHI_DEMO_API_BASE = "https://demo-api.kalshi.co/trade-api/v2"


def _to_float(val) -> float | None:
    """Safely convert API string values like '0.00' to float."""
    if val is None:
        return None
    try:
        return float(val)
    except (ValueError, TypeError):
        return None


class KalshiClient:
    """Minimal Kalshi API client for market data (read-only). Uses RSA key-pair auth."""

    def __init__(
        self,
        api_key_id: str | None = None,
        private_key_path: str | None = None,
        demo: bool = False,
    ):
        self.api_key_id = api_key_id or os.environ.get("KALSHI_API_KEY_ID", "")
        self.private_key_path = private_key_path or os.environ.get("KALSHI_PRIVATE_KEY_PATH", "")
        if demo:
            self.base_url = KALSHI_DEMO_API_BASE
        else:
            self.base_url = os.environ.get("KALSHI_API_BASE_URL", KALSHI_API_BASE_DEFAULT)
        self._private_key = None

        self.session = requests.Session()
        self.session.headers.update({
            "Content-Type": "application/json",
            "User-Agent": "autonomy-platform/1.0",
        })

    def _load_private_key(self):
        """Load RSA private key from PEM file."""
        if self._private_key is not None:
            return self._private_key

        from cryptography.hazmat.primitives.serialization import load_pem_private_key

        key_path = os.path.expanduser(self.private_key_path)
        with open(key_path, "rb") as f:
            self._private_key = load_pem_private_key(f.read(), password=None)
        return self._private_key

    def _sign_request(self, method: str, path: str, timestamp_ms: int) -> str:
        """Generate RSA-SHA256 signature for Kalshi API auth."""
        from cryptography.hazmat.primitives import hashes
        from cryptography.hazmat.primitives.asymmetric import padding

        private_key = self._load_private_key()
        message = f"{timestamp_ms}{method}{path}"
        signature = private_key.sign(
            message.encode("utf-8"),
            padding.PKCS1v15(),
            hashes.SHA256(),
        )
        return base64.b64encode(signature).decode("utf-8")

    def _ensure_auth(self):
        """Validate that credentials are configured."""
        if not self.api_key_id or not self.private_key_path:
            raise ValueError(
                "Kalshi credentials required. Set KALSHI_API_KEY_ID and "
                "KALSHI_PRIVATE_KEY_PATH environment variables."
            )

    def _authed_request(self, method: str, path: str, **kwargs) -> requests.Response:
        """Make an authenticated request to the Kalshi API."""
        self._ensure_auth()

        url = f"{self.base_url}{path}"
        timestamp_ms = int(time.time() * 1000)
        signature = self._sign_request(method.upper(), path, timestamp_ms)

        headers = {
            "KALSHI-ACCESS-KEY": self.api_key_id,
            "KALSHI-ACCESS-SIGNATURE": signature,
            "KALSHI-ACCESS-TIMESTAMP": str(timestamp_ms),
        }
        kwargs.setdefault("timeout", 15)

        resp = self.session.request(method, url, headers=headers, **kwargs)
        resp.raise_for_status()
        return resp

    def get_orderbook(self, ticker: str) -> dict | None:
        """Fetch orderbook for a specific market ticker."""
        try:
            resp = self._authed_request("GET", f"/markets/{ticker}/orderbook", params={"depth": 5})
            data = resp.json()
            # API returns orderbook_fp with yes_dollars/no_dollars (string prices)
            return data.get("orderbook_fp") or data.get("orderbook") or {}
        except requests.RequestException as e:
            logger.warning("Failed to fetch orderbook for %s: %s", ticker, e)
            return None


class OrderbookLogger:
    """Logs Kalshi orderbook snapshots to Postgres using dynamic discovery."""

    def __init__(self, postgres_url: str, kalshi_client: KalshiClient):
        self.db_url = postgres_url
        self.client = kalshi_client
        self._conn = None
        self._discovered_markets: list[dict] = []
        self._last_discovery: float = 0.0
        self._discovery_interval = 3600  # re-discover every hour

    def _get_conn(self):
        if self._conn is None or self._conn.closed:
            self._conn = psycopg2.connect(self.db_url)
            self._conn.autocommit = True
        return self._conn

    def _refresh_discovery(self):
        """Re-discover active weather markets if the cache is stale."""
        from market_logger.discovery import WeatherMarketDiscovery

        now = time.time()
        if self._discovered_markets and now - self._last_discovery < self._discovery_interval:
            return

        discovery = WeatherMarketDiscovery(self.client)
        self._discovered_markets = discovery.get_active_market_tickers()
        self._last_discovery = now

        categories = {}
        for m in self._discovered_markets:
            cat = m.get("_series_category", "unknown")
            categories[cat] = categories.get(cat, 0) + 1

        logger.info(
            "Discovery refreshed: %d markets (%s)",
            len(self._discovered_markets),
            ", ".join(f"{c}={n}" for c, n in sorted(categories.items())),
        )

    def snapshot_all_weather_markets(self):
        """Fetch and store orderbook snapshots for all discovered weather markets."""
        self._refresh_discovery()

        if not self._discovered_markets:
            logger.warning("No weather markets discovered")
            return

        snapshots = []
        now = datetime.now(timezone.utc)

        for market in self._discovered_markets:
            ticker = market.get("ticker", "")
            orderbook = self.client.get_orderbook(ticker)

            if not orderbook:
                continue

            # API returns orderbook_fp with yes_dollars/no_dollars
            # Each entry is [price_str, depth_str] e.g. ["0.52", "194.00"]
            yes_levels = orderbook.get("yes_dollars") or orderbook.get("yes", [])
            no_levels = orderbook.get("no_dollars") or orderbook.get("no", [])

            # Best YES bid = highest yes_dollars price
            # Best YES ask = 1.00 - highest no_dollars price
            bid_price = None
            bid_depth = 0
            ask_price = None
            ask_depth = 0

            if yes_levels:
                best_yes = yes_levels[0]  # first entry = best bid
                bid_price = float(best_yes[0])
                bid_depth = int(float(best_yes[1]))

            if no_levels:
                best_no = no_levels[0]  # first entry = best no bid
                ask_price = 1.0 - float(best_no[0])
                ask_depth = int(float(best_no[1]))

            mid_price = None
            spread = None
            if bid_price is not None and ask_price is not None:
                mid_price = (bid_price + ask_price) / 2
                spread = ask_price - bid_price

            snapshots.append({
                "venue": "kalshi",
                "market_id": ticker,
                "ticker": ticker,
                "title": market.get("title", ""),
                "category": market.get("_series_category", "weather"),
                "bid_price": bid_price,
                "ask_price": ask_price,
                "bid_depth": bid_depth,
                "ask_depth": ask_depth,
                "mid_price": mid_price,
                "spread": spread,
                "volume_24h": _to_float(market.get("volume_24h_fp") or market.get("volume_24h")),
                "open_interest": _to_float(market.get("open_interest_fp") or market.get("open_interest")),
                "expiry": market.get("expiration_time"),
                "captured_at": now,
            })

            # Rate limit
            time.sleep(0.15)

        if snapshots:
            self._insert_snapshots(snapshots)
            logger.info("Logged %d orderbook snapshots", len(snapshots))

    def _insert_snapshots(self, snapshots: list[dict]):
        """Batch insert snapshots into Postgres."""
        conn = self._get_conn()
        cols = [
            "venue", "market_id", "ticker", "title", "category",
            "bid_price", "ask_price", "bid_depth", "ask_depth",
            "mid_price", "spread", "volume_24h", "open_interest",
            "expiry", "captured_at",
        ]

        values_list = []
        for s in snapshots:
            values_list.append(tuple(s.get(c) for c in cols))

        insert_sql = (
            f"INSERT INTO market_data.orderbook_snapshots ({', '.join(cols)}) "
            f"VALUES %s"
        )

        with conn.cursor() as cur:
            psycopg2.extras.execute_values(cur, insert_sql, values_list)

    def log_resolutions(self, markets: list[dict]):
        """Log contract resolutions for settled markets."""
        conn = self._get_conn()

        for market in markets:
            if market.get("result") is not None:
                with conn.cursor() as cur:
                    cur.execute(
                        """
                        INSERT INTO market_data.contract_resolutions
                            (venue, market_id, ticker, title, category, resolved_yes, resolved_at)
                        VALUES (%s, %s, %s, %s, %s, %s, %s)
                        ON CONFLICT DO NOTHING
                        """,
                        (
                            "kalshi",
                            market.get("id", ""),
                            market.get("ticker", ""),
                            market.get("title", ""),
                            "weather",
                            market.get("result") == "yes",
                            market.get("expiration_time"),
                        ),
                    )

    def close(self):
        if self._conn and not self._conn.closed:
            self._conn.close()


def run_logger(postgres_url: str, interval_minutes: int = 5):
    """
    Run the orderbook logger as a long-lived process.

    Discovers weather markets dynamically, then snapshots them every
    `interval_minutes` minutes. Markets are re-discovered hourly.
    """
    client = KalshiClient()
    logger_inst = OrderbookLogger(postgres_url, client)

    def job():
        try:
            logger_inst.snapshot_all_weather_markets()
        except Exception as e:
            logger.error("Snapshot job failed: %s", e, exc_info=True)

    # Run immediately on start
    job()

    # Schedule periodic runs
    schedule.every(interval_minutes).minutes.do(job)

    logger.info("Kalshi orderbook logger started (every %d min)", interval_minutes)

    try:
        while True:
            schedule.run_pending()
            time.sleep(10)
    except KeyboardInterrupt:
        logger.info("Shutting down orderbook logger")
    finally:
        logger_inst.close()


def run_discovery():
    """Run market discovery and print a report (no Postgres needed)."""
    from market_logger.discovery import WeatherMarketDiscovery, print_discovery_report

    client = KalshiClient()
    discovery = WeatherMarketDiscovery(client)
    result = discovery.discover(include_other=True)
    print_discovery_report(result)


def _load_env():
    """Load .env file from python/ dir or repo root, whichever exists."""
    from dotenv import load_dotenv
    from pathlib import Path

    # Try python/.env first (canonical for Python tooling), then repo root
    here = Path(__file__).resolve().parent.parent  # python/
    for candidate in [here / ".env", here.parent / ".env"]:
        if candidate.is_file():
            load_dotenv(candidate, override=False)
            logger.debug("Loaded env from %s", candidate)
            return
    logger.debug("No .env file found")


def _check_credentials():
    """Fail fast with actionable diagnostics if credentials are missing."""
    key_id = os.environ.get("KALSHI_API_KEY_ID", "")
    key_path = os.environ.get("KALSHI_PRIVATE_KEY_PATH", "")
    problems = []

    if not key_id:
        problems.append("KALSHI_API_KEY_ID is not set")
    if not key_path:
        problems.append("KALSHI_PRIVATE_KEY_PATH is not set")
    elif not os.path.isfile(os.path.expanduser(key_path)):
        problems.append(f"KALSHI_PRIVATE_KEY_PATH points to missing file: {key_path}")

    if problems:
        msg = (
            "Kalshi credential check failed:\n"
            + "\n".join(f"  - {p}" for p in problems)
            + "\n\nFix: set these in python/.env or export them in your shell."
        )
        raise SystemExit(msg)


def main():
    import argparse

    parser = argparse.ArgumentParser(description="Kalshi orderbook logger")
    parser.add_argument("--discover", action="store_true",
                        help="Run market discovery only (no logging, no Postgres)")
    parser.add_argument("--postgres-url", default=None,
                        help="Postgres connection URL (or set POSTGRES_URL)")
    parser.add_argument("--interval", type=int, default=5, help="Snapshot interval in minutes")
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()

    logging.basicConfig(
        level=getattr(logging, args.log_level),
        format="%(asctime)s %(name)s %(levelname)s %(message)s",
    )

    _load_env()
    _check_credentials()

    if args.discover:
        run_discovery()
    else:
        postgres_url = args.postgres_url or os.environ.get(
            "POSTGRES_URL",
            "postgresql://trader:localdev@localhost:5432/autonomy?sslmode=disable",
        )
        run_logger(postgres_url, args.interval)


if __name__ == "__main__":
    main()
