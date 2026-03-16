"""
Backtest report generation.

Produces JSON reports and optional matplotlib charts for the Phase 1
go/no-go decision. Reports include reliability diagrams, P&L curves,
and edge distributions.
"""

import json
import logging
from datetime import datetime
from pathlib import Path

import numpy as np

logger = logging.getLogger(__name__)


def save_json_report(result, output_path: str = "reports/backtest_report.json") -> str:
    """
    Save a backtest result as a JSON report.

    Args:
        result: BacktestResult from BacktestRunner.run()
        output_path: Path for the JSON output

    Returns:
        Path to the saved report.
    """
    Path(output_path).parent.mkdir(parents=True, exist_ok=True)

    report = {
        "generated_at": datetime.utcnow().isoformat(),
        "model_version": result.model_version,
        "period": {
            "train_start": result.train_start,
            "train_end": result.train_end,
            "test_start": result.test_start,
            "test_end": result.test_end,
        },
        "accuracy": {
            "brier_score": result.brier_score,
            "brier_skill_score": result.brier_skill_score,
            "mean_crps": result.mean_crps,
            "ece": result.ece,
        },
        "trading": {
            "total_pnl": result.total_pnl,
            "sharpe_ratio": result.sharpe_ratio,
            "max_drawdown": result.max_drawdown,
            "max_drawdown_pct": result.max_drawdown_pct,
            "win_rate": result.win_rate,
            "profit_factor": result.profit_factor,
            "num_trades": result.num_trades,
            "avg_edge": result.avg_edge,
        },
        "monthly_pnl": result.monthly_pnl,
        "positive_months_pct": result.positive_months_pct,
        "edge_distribution": result.edge_stats,
        "reliability": result.reliability,
        "go_no_go": {
            "brier_pass": result.brier_score < 0.20,
            "sharpe_pass": result.sharpe_ratio > 1.0,
            "months_pass": result.positive_months_pct >= 0.60,
            "edge_pass": result.avg_edge > 0.03,
            "verdict": (
                "GO"
                if (
                    result.brier_score < 0.20
                    and result.sharpe_ratio > 1.0
                    and result.positive_months_pct >= 0.60
                    and result.avg_edge > 0.03
                )
                else "NO-GO"
            ),
        },
    }

    with open(output_path, "w") as f:
        json.dump(report, f, indent=2, default=str)

    logger.info("Report saved to %s", output_path)
    return output_path


def plot_reliability_diagram(result, output_path: str = "reports/reliability_diagram.png"):
    """
    Plot a reliability (calibration) diagram.

    Shows predicted probability vs. observed frequency.
    A perfectly calibrated model follows the diagonal.
    """
    try:
        import matplotlib.pyplot as plt
    except ImportError:
        logger.warning("matplotlib not available, skipping reliability diagram")
        return

    Path(output_path).parent.mkdir(parents=True, exist_ok=True)

    reliability = result.reliability
    if not reliability or not reliability.get("bin_centers"):
        logger.warning("No reliability data available")
        return

    fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(8, 10), height_ratios=[3, 1])

    # Main reliability diagram
    ax1.plot([0, 1], [0, 1], "k--", label="Perfect calibration", alpha=0.5)
    ax1.plot(
        reliability["predicted_mean"],
        reliability["observed_freq"],
        "bo-",
        label="Model",
        markersize=8,
    )
    ax1.set_xlabel("Predicted Probability")
    ax1.set_ylabel("Observed Frequency")
    ax1.set_title(f"Reliability Diagram — {result.model_version}")
    ax1.set_xlim(0, 1)
    ax1.set_ylim(0, 1)
    ax1.legend()
    ax1.grid(True, alpha=0.3)

    # Histogram of prediction counts per bin
    ax2.bar(
        reliability["bin_centers"],
        reliability["counts"],
        width=0.08,
        alpha=0.7,
        color="steelblue",
    )
    ax2.set_xlabel("Predicted Probability")
    ax2.set_ylabel("Count")
    ax2.set_title("Prediction Distribution")
    ax2.set_xlim(0, 1)

    plt.tight_layout()
    plt.savefig(output_path, dpi=150, bbox_inches="tight")
    plt.close()
    logger.info("Reliability diagram saved to %s", output_path)


def plot_pnl_curve(result, output_path: str = "reports/pnl_curve.png"):
    """Plot cumulative P&L over time."""
    try:
        import matplotlib.pyplot as plt
    except ImportError:
        logger.warning("matplotlib not available, skipping P&L curve")
        return

    if not result.trades:
        return

    Path(output_path).parent.mkdir(parents=True, exist_ok=True)

    pnls = [t.pnl for t in result.trades]
    cumulative = np.cumsum(pnls)

    fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(12, 8))

    # Cumulative P&L
    ax1.plot(cumulative, color="darkgreen", linewidth=1.5)
    ax1.axhline(y=0, color="black", linewidth=0.5)
    ax1.fill_between(range(len(cumulative)), cumulative, alpha=0.2, color="green")
    ax1.set_title(f"Cumulative P&L — {result.model_version}")
    ax1.set_ylabel("P&L ($)")
    ax1.set_xlabel("Trade #")
    ax1.grid(True, alpha=0.3)

    # Per-trade P&L
    colors = ["green" if p > 0 else "red" for p in pnls]
    ax2.bar(range(len(pnls)), pnls, color=colors, alpha=0.6, width=1.0)
    ax2.axhline(y=0, color="black", linewidth=0.5)
    ax2.set_title("Per-Trade P&L")
    ax2.set_ylabel("P&L ($)")
    ax2.set_xlabel("Trade #")
    ax2.grid(True, alpha=0.3)

    plt.tight_layout()
    plt.savefig(output_path, dpi=150, bbox_inches="tight")
    plt.close()
    logger.info("P&L curve saved to %s", output_path)


def plot_edge_histogram(result, output_path: str = "reports/edge_distribution.png"):
    """Plot distribution of trading edge across all trades."""
    try:
        import matplotlib.pyplot as plt
    except ImportError:
        return

    if not result.trades:
        return

    Path(output_path).parent.mkdir(parents=True, exist_ok=True)

    edges = [t.edge for t in result.trades]

    fig, ax = plt.subplots(figsize=(8, 5))
    ax.hist(edges, bins=30, color="steelblue", alpha=0.7, edgecolor="black")
    ax.axvline(x=np.mean(edges), color="red", linestyle="--", label=f"Mean: {np.mean(edges):.4f}")
    ax.axvline(x=np.median(edges), color="orange", linestyle="--", label=f"Median: {np.median(edges):.4f}")
    ax.set_xlabel("Net Edge (after fees)")
    ax.set_ylabel("Count")
    ax.set_title(f"Edge Distribution — {result.model_version}")
    ax.legend()
    ax.grid(True, alpha=0.3)

    plt.tight_layout()
    plt.savefig(output_path, dpi=150, bbox_inches="tight")
    plt.close()
    logger.info("Edge distribution saved to %s", output_path)


def generate_full_report(result, output_dir: str = "reports"):
    """Generate all report artifacts (JSON + charts)."""
    save_json_report(result, f"{output_dir}/backtest_report.json")
    plot_reliability_diagram(result, f"{output_dir}/reliability_diagram.png")
    plot_pnl_curve(result, f"{output_dir}/pnl_curve.png")
    plot_edge_histogram(result, f"{output_dir}/edge_distribution.png")
    logger.info("Full report generated in %s/", output_dir)
