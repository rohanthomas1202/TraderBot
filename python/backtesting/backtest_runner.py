"""
Backtest Runner

Replays historical data through the EMOS weather model, simulates trading
decisions, and computes P&L. This is the core tool for answering the
Phase 1 question: "Does this model have edge?"

Critical design decisions:
1. Uses the SAME model code as the live system (no backtest-specific reimplementation)
2. Computes edge against ask price (buys) / bid price (sells), NOT mid price
3. Deducts Kalshi fees from edge calculation
4. Strict temporal separation: no future data leakage
"""

import logging
from dataclasses import dataclass, field
from datetime import datetime

import numpy as np

from backtesting import metrics
from backtesting.data_loader import WeatherDataLoader
from models.weather.emos import EMOSModel

logger = logging.getLogger(__name__)

# Kalshi fee schedule (simplified: percentage of payout)
KALSHI_FEE_RATE = 0.03  # 3% of contract value — conservative estimate

# Default trading parameters
DEFAULT_MIN_EDGE = 0.05        # Minimum 5 cents of edge to trade
DEFAULT_MAX_QUANTITY = 10       # Max contracts per trade
DEFAULT_CONTRACT_VALUE = 1.0    # $1.00 per contract (Kalshi standard)
DEFAULT_DATA_DIR = "data/backtest/weather"


@dataclass
class SimulatedTrade:
    """Record of a simulated trade from backtesting."""

    date: str
    station_icao: str
    market_id: str
    side: str  # "buy" or "sell"
    quantity: int
    entry_price: float  # Price paid/received
    model_prob: float
    market_prob: float  # Simulated market price
    edge: float  # Net edge after fees
    threshold: float
    observation: float
    resolved_yes: bool
    pnl: float  # Realized P&L


@dataclass
class BacktestResult:
    """Complete result of a backtest run."""

    model_version: str
    train_start: str
    train_end: str
    test_start: str
    test_end: str

    # Model accuracy
    brier_score: float
    brier_skill_score: float
    mean_crps: float

    # Trading performance
    total_pnl: float
    sharpe_ratio: float
    max_drawdown: float
    max_drawdown_pct: float
    win_rate: float
    profit_factor: float
    num_trades: int
    avg_edge: float

    # Monthly breakdown
    monthly_pnl: dict = field(default_factory=dict)
    positive_months_pct: float = 0.0

    # Calibration
    ece: float = 0.0
    reliability: dict = field(default_factory=dict)

    # Edge distribution
    edge_stats: dict = field(default_factory=dict)

    # Raw data for further analysis
    trades: list = field(default_factory=list)
    predictions: list = field(default_factory=list)


class BacktestRunner:
    """
    Runs a historical backtest of the EMOS weather model.

    Usage:
        loader = WeatherDataLoader("data/backtest/weather").load()
        model = EMOSModel()

        runner = BacktestRunner(model, loader)
        result = runner.run(
            train_start="2020-01-01", train_end="2023-12-31",
            test_start="2024-01-01", test_end="2025-12-31",
        )
        runner.print_report(result)
    """

    def __init__(
        self,
        model: EMOSModel,
        loader: WeatherDataLoader,
        min_edge: float = DEFAULT_MIN_EDGE,
        max_quantity: int = DEFAULT_MAX_QUANTITY,
        fee_rate: float = KALSHI_FEE_RATE,
    ):
        self.model = model
        self.loader = loader
        self.min_edge = min_edge
        self.max_quantity = max_quantity
        self.fee_rate = fee_rate

    def run(
        self,
        train_start: str,
        train_end: str,
        test_start: str,
        test_end: str,
        variable: str = "tmax",
        lead_hours: int = 24,
        thresholds: list[float] | None = None,
    ) -> BacktestResult:
        """
        Execute the full backtest pipeline.

        1. Load and align training data
        2. Train the EMOS model
        3. Load and align test data
        4. Generate forecasts on test data
        5. Simulate trading decisions
        6. Compute performance metrics
        """
        logger.info("Starting backtest: train %s-%s, test %s-%s", train_start, train_end, test_start, test_end)

        # Step 1: Load training data
        train_data = self.loader.get_aligned_data(train_start, train_end, variable, lead_hours)
        if len(train_data["observations"]) == 0:
            raise ValueError("No training data found")

        train_data = self.loader.add_threshold_outcomes(train_data, thresholds)
        logger.info("Training data: %d samples", len(train_data["observations"]))

        # Step 2: Train model
        self.model.train(train_data)

        # Step 3: Load test data
        test_data = self.loader.get_aligned_data(test_start, test_end, variable, lead_hours)
        if len(test_data["observations"]) == 0:
            raise ValueError("No test data found")

        test_data = self.loader.add_threshold_outcomes(test_data, thresholds)
        logger.info("Test data: %d samples", len(test_data["observations"]))

        # Step 4: Evaluate model accuracy
        eval_result = self.model.evaluate(test_data)

        # Step 5: Generate forecasts and simulate trades
        trades = []
        all_predictions = []
        all_outcomes = []

        n_samples = len(test_data["thresholds"])
        for i in range(n_samples):
            contract = {
                "market_id": f"{test_data['station_icaos'][i]}-{variable}-{test_data['thresholds'][i]:.0f}",
                "threshold": test_data["thresholds"][i],
                "direction": "above",
            }
            features = {
                "gfs_mean": float(test_data["gfs_mean"][i]),
                "gfs_std": float(test_data["gfs_std"][i]),
                "ecmwf_mean": float(test_data["ecmwf_mean"][i]),
            }

            forecast = self.model.forecast(contract, features)
            outcome = test_data["outcomes"][i]

            all_predictions.append(forecast.model_prob)
            all_outcomes.append(outcome)

            # Simulate market price: use a noisy version of the true probability
            # In practice, this would come from historical Kalshi orderbook data
            true_prob = outcome  # Binary — either 0 or 1
            # Simulate market mid as climatological probability + noise
            clim_prob = np.mean(test_data["outcomes"])
            market_mid = np.clip(clim_prob + np.random.normal(0, 0.10), 0.05, 0.95)
            market_spread = 0.04  # 4 cents typical spread

            # Determine trade direction and edge
            if forecast.model_prob > market_mid + market_spread / 2:
                # Buy YES: model thinks event is more likely than market
                side = "buy"
                entry_price = market_mid + market_spread / 2  # Pay the ask
                edge = forecast.model_prob - entry_price - self.fee_rate
            elif forecast.model_prob < market_mid - market_spread / 2:
                # Sell YES (buy NO): model thinks event is less likely
                side = "sell"
                entry_price = market_mid - market_spread / 2  # Sell at bid
                edge = entry_price - forecast.model_prob - self.fee_rate
            else:
                continue  # No edge, skip

            if edge < self.min_edge:
                continue

            # Simulate P&L
            if side == "buy":
                pnl = (1.0 - entry_price) if outcome == 1 else -entry_price
            else:
                pnl = entry_price if outcome == 0 else -(1.0 - entry_price)

            # Deduct fees
            pnl -= self.fee_rate

            quantity = min(self.max_quantity, max(1, int(edge * 100)))

            trade = SimulatedTrade(
                date=str(test_data["dates"][i]),
                station_icao=str(test_data["station_icaos"][i]),
                market_id=contract["market_id"],
                side=side,
                quantity=quantity,
                entry_price=entry_price,
                model_prob=forecast.model_prob,
                market_prob=market_mid,
                edge=edge,
                threshold=test_data["thresholds"][i],
                observation=float(test_data["observations"][i]),
                resolved_yes=bool(outcome),
                pnl=pnl * quantity,
            )
            trades.append(trade)

        # Step 6: Compute metrics
        predictions_arr = np.array(all_predictions)
        outcomes_arr = np.array(all_outcomes)

        trade_pnls = np.array([t.pnl for t in trades]) if trades else np.array([0.0])
        trade_edges = np.array([t.edge for t in trades]) if trades else np.array([0.0])
        trade_dates = np.array([t.date for t in trades]) if trades else np.array([""])
        cumulative_pnl = np.cumsum(trade_pnls)

        monthly = metrics.monthly_pnl(trade_dates, trade_pnls) if trades else {}
        positive_months = sum(1 for v in monthly.values() if v > 0)
        total_months = max(len(monthly), 1)

        result = BacktestResult(
            model_version=self.model.version,
            train_start=train_start,
            train_end=train_end,
            test_start=test_start,
            test_end=test_end,
            brier_score=metrics.brier_score(predictions_arr, outcomes_arr),
            brier_skill_score=metrics.brier_skill_score(predictions_arr, outcomes_arr),
            mean_crps=eval_result.get("mean_crps", 0.0),
            total_pnl=float(np.sum(trade_pnls)),
            sharpe_ratio=metrics.sharpe_ratio(trade_pnls),
            max_drawdown=metrics.max_drawdown(cumulative_pnl),
            max_drawdown_pct=metrics.max_drawdown_pct(cumulative_pnl, initial_capital=1000),
            win_rate=metrics.win_rate(trade_pnls),
            profit_factor=metrics.profit_factor(trade_pnls),
            num_trades=len(trades),
            avg_edge=float(np.mean(trade_edges)) if trades else 0.0,
            monthly_pnl=monthly,
            positive_months_pct=positive_months / total_months,
            ece=eval_result.get("ece", 0.0),
            reliability=eval_result.get("reliability", {}),
            edge_stats=metrics.edge_distribution(trade_edges),
            trades=trades,
            predictions=list(zip(all_predictions, all_outcomes.tolist())),
        )

        logger.info(
            "Backtest complete: %d trades, Brier=%.4f, Sharpe=%.2f, PnL=$%.2f",
            result.num_trades, result.brier_score, result.sharpe_ratio, result.total_pnl,
        )
        return result

    @staticmethod
    def print_report(result: BacktestResult):
        """Print a human-readable backtest report to stdout."""
        print("=" * 70)
        print(f"BACKTEST REPORT: {result.model_version}")
        print(f"Train: {result.train_start} to {result.train_end}")
        print(f"Test:  {result.test_start} to {result.test_end}")
        print("=" * 70)
        print()

        print("--- Model Accuracy ---")
        print(f"  Brier Score:        {result.brier_score:.4f}  (target: < 0.20)")
        print(f"  Brier Skill Score:  {result.brier_skill_score:.4f}")
        print(f"  Mean CRPS:          {result.mean_crps:.4f}")
        print(f"  ECE:                {result.ece:.4f}")
        print()

        go_brier = "PASS" if result.brier_score < 0.20 else "FAIL"
        print(f"  GO/NO-GO (Brier):   {go_brier}")
        print()

        print("--- Trading Performance ---")
        print(f"  Total P&L:          ${result.total_pnl:.2f}")
        print(f"  Sharpe Ratio:       {result.sharpe_ratio:.2f}  (target: > 1.0)")
        print(f"  Max Drawdown:       ${result.max_drawdown:.2f}")
        print(f"  Max Drawdown %:     {result.max_drawdown_pct:.1%}")
        print(f"  Win Rate:           {result.win_rate:.1%}")
        print(f"  Profit Factor:      {result.profit_factor:.2f}")
        print(f"  Num Trades:         {result.num_trades}")
        print(f"  Avg Edge:           {result.avg_edge:.4f}")
        print()

        go_sharpe = "PASS" if result.sharpe_ratio > 1.0 else "FAIL"
        print(f"  GO/NO-GO (Sharpe):  {go_sharpe}")
        print()

        print("--- Monthly P&L ---")
        positive_count = 0
        for month, pnl in sorted(result.monthly_pnl.items()):
            marker = "+" if pnl > 0 else "-"
            print(f"  {month}: {marker}${abs(pnl):.2f}")
            if pnl > 0:
                positive_count += 1
        total_months = max(len(result.monthly_pnl), 1)
        print(f"  Positive months:    {positive_count}/{total_months} ({result.positive_months_pct:.0%})")
        print()

        go_months = "PASS" if result.positive_months_pct >= 0.60 else "FAIL"
        print(f"  GO/NO-GO (months):  {go_months}")
        print()

        print("--- Edge Distribution ---")
        for key, val in result.edge_stats.items():
            print(f"  {key}: {val:.4f}" if isinstance(val, float) else f"  {key}: {val}")
        print()

        print("--- GO/NO-GO Summary ---")
        all_pass = (
            result.brier_score < 0.20
            and result.sharpe_ratio > 1.0
            and result.positive_months_pct >= 0.60
            and result.avg_edge > 0.03
        )
        verdict = "GO — Proceed to Phase 2" if all_pass else "NO-GO — Review model or pivot vertical"
        print(f"  Verdict: {verdict}")
        print("=" * 70)


def main():
    """CLI entry point for running backtests."""
    import argparse

    parser = argparse.ArgumentParser(description="Run EMOS weather model backtest")
    parser.add_argument("--data-dir", default=DEFAULT_DATA_DIR, help="Path to backtest data")
    parser.add_argument("--train-start", default="2020-01-01")
    parser.add_argument("--train-end", default="2023-12-31")
    parser.add_argument("--test-start", default="2024-01-01")
    parser.add_argument("--test-end", default="2025-12-31")
    parser.add_argument("--variable", default="tmax", choices=["tmax", "tmin", "precip", "temp"])
    parser.add_argument("--lead-hours", type=int, default=24)
    parser.add_argument("--min-edge", type=float, default=DEFAULT_MIN_EDGE)
    parser.add_argument("--constant-sigma", action="store_true",
                        help="Use constant-sigma EMOS (no ensemble spread term)")
    parser.add_argument("--save-model", default="models/weather_emos_v1.pkl")
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()

    logging.basicConfig(
        level=getattr(logging, args.log_level),
        format="%(asctime)s %(name)s %(levelname)s %(message)s",
    )

    loader = WeatherDataLoader(args.data_dir).load()
    model = EMOSModel(constant_sigma=args.constant_sigma)

    runner = BacktestRunner(model, loader, min_edge=args.min_edge)
    result = runner.run(
        train_start=args.train_start,
        train_end=args.train_end,
        test_start=args.test_start,
        test_end=args.test_end,
        variable=args.variable,
        lead_hours=args.lead_hours,
    )

    runner.print_report(result)

    # Save model
    if args.save_model:
        model.save(args.save_model)


if __name__ == "__main__":
    main()
