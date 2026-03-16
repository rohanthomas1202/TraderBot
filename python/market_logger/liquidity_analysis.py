"""
Kalshi Weather Market Liquidity Analysis

Analyzes orderbook snapshots collected by the Kalshi logger to determine
whether there is enough liquidity to trade profitably. This is a key
input to the Phase 1 go/no-go decision.

Run after 10+ days of orderbook logging:
    python -m market_logger.liquidity_analysis --postgres-url $POSTGRES_URL
"""

import json
import logging
import os
from datetime import datetime

import numpy as np
import pandas as pd
import psycopg2

logger = logging.getLogger(__name__)


WEATHER_CATEGORIES = ("daily_high", "daily_low", "rain", "snow")


def load_snapshots(postgres_url: str) -> pd.DataFrame:
    """Load all weather orderbook snapshots from Postgres."""
    conn = psycopg2.connect(postgres_url)
    cur = conn.cursor()
    cur.execute(
        """
        SELECT
            market_id, ticker, title, category,
            bid_price, ask_price, bid_depth, ask_depth,
            mid_price, spread, volume_24h, open_interest,
            expiry, captured_at
        FROM market_data.orderbook_snapshots
        WHERE category = ANY(%s)
        ORDER BY captured_at
        """,
        (list(WEATHER_CATEGORIES),),
    )
    cols = [desc[0] for desc in cur.description]
    rows = cur.fetchall()
    conn.close()
    return pd.DataFrame(rows, columns=cols)


def analyze_liquidity(df: pd.DataFrame) -> dict:
    """
    Compute liquidity metrics from orderbook snapshots.

    Returns a dict with metrics for the go/no-go decision:
    - active_contracts: number of unique weather contracts
    - median_depth: median orderbook depth at best bid/ask
    - median_spread: median bid-ask spread
    - depth_distribution: percentiles of depth
    - spread_distribution: percentiles of spread
    - volume_stats: average daily volume per contract
    - time_coverage: how many days of data we have
    """
    if df.empty:
        return {"error": "No orderbook data found"}

    # Unique contracts
    unique_contracts = df["market_id"].nunique()

    # Most recent snapshot per contract for "active" count
    latest = df.sort_values("captured_at").groupby("market_id").last()
    active_contracts = len(latest)

    # Depth statistics (in contracts)
    bid_depths = df["bid_depth"].dropna()
    ask_depths = df["ask_depth"].dropna()
    total_depths = bid_depths + ask_depths

    # Spread statistics
    spreads = df["spread"].dropna()

    # Volume statistics
    volumes = df.groupby("market_id")["volume_24h"].mean()

    # Time coverage
    date_range = (df["captured_at"].max() - df["captured_at"].min()).days

    # Compute depth in dollar terms (depth * mid_price)
    df["bid_depth_dollars"] = df["bid_depth"] * df.get("bid_price", df["mid_price"]).fillna(0.5)
    df["ask_depth_dollars"] = df["ask_depth"] * (1 - df.get("ask_price", df["mid_price"]).fillna(0.5))
    dollar_depths = df["bid_depth_dollars"] + df["ask_depth_dollars"]

    result = {
        "summary": {
            "unique_contracts": int(unique_contracts),
            "active_contracts": int(active_contracts),
            "total_snapshots": len(df),
            "days_of_data": int(date_range),
            "date_range": f"{df['captured_at'].min()} to {df['captured_at'].max()}",
        },
        "depth_contracts": {
            "median": float(total_depths.median()) if not total_depths.empty else 0,
            "mean": float(total_depths.mean()) if not total_depths.empty else 0,
            "p10": float(total_depths.quantile(0.10)) if not total_depths.empty else 0,
            "p25": float(total_depths.quantile(0.25)) if not total_depths.empty else 0,
            "p75": float(total_depths.quantile(0.75)) if not total_depths.empty else 0,
            "p90": float(total_depths.quantile(0.90)) if not total_depths.empty else 0,
        },
        "depth_dollars": {
            "median": float(dollar_depths.median()) if not dollar_depths.empty else 0,
            "mean": float(dollar_depths.mean()) if not dollar_depths.empty else 0,
            "p10": float(dollar_depths.quantile(0.10)) if not dollar_depths.empty else 0,
            "p90": float(dollar_depths.quantile(0.90)) if not dollar_depths.empty else 0,
        },
        "spread": {
            "median": float(spreads.median()) if not spreads.empty else 0,
            "mean": float(spreads.mean()) if not spreads.empty else 0,
            "p10": float(spreads.quantile(0.10)) if not spreads.empty else 0,
            "p25": float(spreads.quantile(0.25)) if not spreads.empty else 0,
            "p75": float(spreads.quantile(0.75)) if not spreads.empty else 0,
            "p90": float(spreads.quantile(0.90)) if not spreads.empty else 0,
        },
        "volume": {
            "mean_per_contract": float(volumes.mean()) if not volumes.empty else 0,
            "median_per_contract": float(volumes.median()) if not volumes.empty else 0,
            "total_daily": float(volumes.sum()) if not volumes.empty else 0,
        },
        "go_no_go": {},
    }

    # Apply go/no-go criteria
    result["go_no_go"] = {
        "active_contracts_pass": active_contracts >= 20,
        "active_contracts_value": active_contracts,
        "active_contracts_threshold": 20,
        "depth_pass": result["depth_dollars"]["median"] >= 100,
        "depth_value": result["depth_dollars"]["median"],
        "depth_threshold": 100,
        "spread_pass": result["spread"]["median"] <= 0.05,
        "spread_value": result["spread"]["median"],
        "spread_threshold": 0.05,
        "verdict": (
            "GO"
            if (active_contracts >= 20 and result["depth_dollars"]["median"] >= 100 and result["spread"]["median"] <= 0.05)
            else "NO-GO"
        ),
    }

    return result


def per_contract_summary(df: pd.DataFrame) -> pd.DataFrame:
    """Summary statistics per contract for detailed analysis."""
    if df.empty:
        return pd.DataFrame()

    summary = df.groupby(["market_id", "ticker", "title"]).agg(
        snapshots=("captured_at", "count"),
        avg_mid_price=("mid_price", "mean"),
        avg_spread=("spread", "mean"),
        avg_bid_depth=("bid_depth", "mean"),
        avg_ask_depth=("ask_depth", "mean"),
        avg_volume_24h=("volume_24h", "mean"),
        avg_open_interest=("open_interest", "mean"),
    ).reset_index()

    summary = summary.sort_values("avg_volume_24h", ascending=False)
    return summary


def print_report(analysis: dict, per_contract: pd.DataFrame):
    """Print human-readable liquidity report."""
    print("=" * 70)
    print("KALSHI WEATHER MARKET LIQUIDITY ANALYSIS")
    print("=" * 70)
    print()

    if "error" in analysis:
        print(f"  ERROR: {analysis['error']}")
        print("=" * 70)
        return

    s = analysis["summary"]
    print(f"Data period:       {s['date_range']}")
    print(f"Days of data:      {s['days_of_data']}")
    print(f"Total snapshots:   {s['total_snapshots']}")
    print(f"Active contracts:  {s['active_contracts']}")
    print()

    print("--- Orderbook Depth (contracts) ---")
    d = analysis["depth_contracts"]
    print(f"  Median: {d['median']:.0f}  Mean: {d['mean']:.0f}")
    print(f"  P10: {d['p10']:.0f}  P25: {d['p25']:.0f}  P75: {d['p75']:.0f}  P90: {d['p90']:.0f}")
    print()

    print("--- Orderbook Depth ($) ---")
    d = analysis["depth_dollars"]
    print(f"  Median: ${d['median']:.0f}  Mean: ${d['mean']:.0f}")
    print(f"  P10: ${d['p10']:.0f}  P90: ${d['p90']:.0f}")
    print()

    print("--- Bid-Ask Spread ---")
    sp = analysis["spread"]
    print(f"  Median: {sp['median']:.4f}  Mean: {sp['mean']:.4f}")
    print(f"  P10: {sp['p10']:.4f}  P25: {sp['p25']:.4f}  P75: {sp['p75']:.4f}  P90: {sp['p90']:.4f}")
    print()

    print("--- Volume ---")
    v = analysis["volume"]
    print(f"  Mean per contract: {v['mean_per_contract']:.0f}")
    print(f"  Estimated total daily: {v['total_daily']:.0f}")
    print()

    print("--- GO/NO-GO ---")
    g = analysis["go_no_go"]
    print(f"  Active contracts >= 20: {'PASS' if g['active_contracts_pass'] else 'FAIL'} ({g['active_contracts_value']})")
    print(f"  Median depth >= $100:   {'PASS' if g['depth_pass'] else 'FAIL'} (${g['depth_value']:.0f})")
    print(f"  Median spread <= $0.05: {'PASS' if g['spread_pass'] else 'FAIL'} (${g['spread_value']:.4f})")
    print(f"  Verdict: {g['verdict']}")
    print()

    if not per_contract.empty:
        print("--- Top 15 Contracts by Volume ---")
        top = per_contract.head(15)
        for _, row in top.iterrows():
            print(f"  {row['ticker']:30s} mid={row['avg_mid_price']:.2f} "
                  f"spread={row['avg_spread']:.3f} depth={row['avg_bid_depth']:.0f}/{row['avg_ask_depth']:.0f} "
                  f"vol={row['avg_volume_24h']:.0f}")

    print("=" * 70)


def main():
    import argparse
    from dotenv import load_dotenv
    from pathlib import Path

    # Load .env from python/ dir or repo root
    here = Path(__file__).resolve().parent.parent
    for candidate in [here / ".env", here.parent / ".env"]:
        if candidate.is_file():
            load_dotenv(candidate, override=False)
            break

    parser = argparse.ArgumentParser(description="Analyze Kalshi weather market liquidity")
    parser.add_argument("--postgres-url", default=os.environ.get(
        "POSTGRES_URL",
        "postgresql://trader:localdev@localhost:5432/autonomy?sslmode=disable",
    ))
    parser.add_argument("--json-output", default="reports/liquidity_report.json")
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()

    logging.basicConfig(level=getattr(logging, args.log_level))

    df = load_snapshots(args.postgres_url)
    analysis = analyze_liquidity(df)
    per_contract = per_contract_summary(df)

    print_report(analysis, per_contract)

    # Save JSON report
    Path(args.json_output).parent.mkdir(parents=True, exist_ok=True)
    with open(args.json_output, "w") as f:
        json.dump(analysis, f, indent=2, default=str)
    print(f"\nJSON report saved to {args.json_output}")


if __name__ == "__main__":
    main()
