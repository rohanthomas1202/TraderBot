"""
Backtesting metrics for evaluating model accuracy and trading profitability.

All scoring functions follow the convention:
- Lower is better for error metrics (Brier, CRPS, MAE)
- Higher is better for profitability metrics (Sharpe, profit factor, win rate)
"""

import numpy as np


def brier_score(predictions: np.ndarray, outcomes: np.ndarray) -> float:
    """
    Brier Score: mean squared error of probability forecasts.

    BS = mean((p - o)^2) where p is predicted probability and o is binary outcome.
    Range: [0, 1]. Lower is better.
    - 0.0 = perfect forecasts
    - 0.25 = random guessing (always predicting 0.5)
    - 0.33 = always predicting the wrong class with 50% confidence

    Target: < 0.20 for Phase 1 go/no-go.
    """
    predictions = np.asarray(predictions, dtype=np.float64)
    outcomes = np.asarray(outcomes, dtype=np.float64)
    return float(np.mean((predictions - outcomes) ** 2))


def brier_skill_score(predictions: np.ndarray, outcomes: np.ndarray) -> float:
    """
    Brier Skill Score: improvement over climatological baseline.

    BSS = 1 - BS / BS_clim where BS_clim uses the sample mean as prediction.
    Range: (-inf, 1]. Higher is better.
    - 1.0 = perfect
    - 0.0 = no skill over climatology
    - < 0 = worse than climatology
    """
    bs = brier_score(predictions, outcomes)
    clim_prob = np.mean(outcomes)
    bs_clim = brier_score(np.full_like(predictions, clim_prob), outcomes)
    if bs_clim == 0:
        return 0.0
    return float(1 - bs / bs_clim)


def sharpe_ratio(pnls: np.ndarray, annualization_factor: float = 252.0) -> float:
    """
    Annualized Sharpe ratio from daily P&L series.

    Sharpe = sqrt(252) * mean(daily_returns) / std(daily_returns)

    Target: > 1.0 for Phase 1 go/no-go.
    """
    pnls = np.asarray(pnls, dtype=np.float64)
    if len(pnls) < 2 or np.std(pnls) == 0:
        return 0.0
    return float(np.sqrt(annualization_factor) * np.mean(pnls) / np.std(pnls))


def max_drawdown(cumulative_pnl: np.ndarray) -> float:
    """
    Maximum drawdown from peak cumulative P&L.

    Returns the largest peak-to-trough decline as a positive number.
    """
    cumulative_pnl = np.asarray(cumulative_pnl, dtype=np.float64)
    if len(cumulative_pnl) == 0:
        return 0.0
    running_max = np.maximum.accumulate(cumulative_pnl)
    drawdowns = running_max - cumulative_pnl
    return float(np.max(drawdowns))


def max_drawdown_pct(cumulative_pnl: np.ndarray, initial_capital: float = 1.0) -> float:
    """Max drawdown as percentage of peak equity."""
    cumulative_pnl = np.asarray(cumulative_pnl, dtype=np.float64)
    equity = initial_capital + cumulative_pnl
    if len(equity) == 0 or np.max(equity) <= 0:
        return 0.0
    running_max = np.maximum.accumulate(equity)
    drawdown_pct = (running_max - equity) / running_max
    return float(np.max(drawdown_pct))


def win_rate(trade_pnls: np.ndarray) -> float:
    """Fraction of trades with positive P&L."""
    trade_pnls = np.asarray(trade_pnls, dtype=np.float64)
    if len(trade_pnls) == 0:
        return 0.0
    return float(np.mean(trade_pnls > 0))


def profit_factor(trade_pnls: np.ndarray) -> float:
    """
    Gross profit / gross loss.

    > 1.0 means profitable. Higher is better.
    """
    trade_pnls = np.asarray(trade_pnls, dtype=np.float64)
    gross_profit = np.sum(trade_pnls[trade_pnls > 0])
    gross_loss = abs(np.sum(trade_pnls[trade_pnls < 0]))
    if gross_loss == 0:
        return float("inf") if gross_profit > 0 else 0.0
    return float(gross_profit / gross_loss)


def monthly_pnl(dates: np.ndarray, trade_pnls: np.ndarray) -> dict[str, float]:
    """
    Aggregate P&L by calendar month.

    Args:
        dates: Array of date strings (YYYY-MM-DD).
        trade_pnls: Corresponding P&L values.

    Returns:
        Dict mapping 'YYYY-MM' to total P&L for that month.
    """
    import pandas as pd

    df = pd.DataFrame({"date": pd.to_datetime(dates), "pnl": trade_pnls})
    monthly = df.groupby(df["date"].dt.to_period("M"))["pnl"].sum()
    return {str(k): float(v) for k, v in monthly.items()}


def edge_distribution(edges: np.ndarray) -> dict:
    """Summary statistics of trading edge distribution."""
    edges = np.asarray(edges, dtype=np.float64)
    if len(edges) == 0:
        return {"mean": 0, "median": 0, "std": 0, "p10": 0, "p90": 0, "count": 0}
    return {
        "mean": float(np.mean(edges)),
        "median": float(np.median(edges)),
        "std": float(np.std(edges)),
        "p10": float(np.percentile(edges, 10)),
        "p90": float(np.percentile(edges, 90)),
        "count": len(edges),
    }
