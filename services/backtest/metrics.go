package backtest

import (
	"math"
)

// Trade records a single completed trade for metric computation.
type Trade struct {
	MarketID    string
	Side        string
	Quantity    int32
	PriceMicros int64
	PnLMicros   int64
}

// BacktestMetrics holds computed performance metrics.
type BacktestMetrics struct {
	TotalReturn  float64
	SharpeRatio  float64
	MaxDrawdown  float64
	WinRate      float64
	ProfitFactor float64
	TradeCount   int
	TotalPnL     int64
}

// ComputeFromTrades computes all metrics from a trade log and equity curve.
func ComputeFromTrades(trades []Trade, equityCurve []int64, initialCapital int64) BacktestMetrics {
	m := BacktestMetrics{TradeCount: len(trades)}
	if len(trades) == 0 {
		return m
	}

	var totalPnL int64
	var wins, losses int
	var grossProfit, grossLoss int64

	for _, t := range trades {
		totalPnL += t.PnLMicros
		if t.PnLMicros > 0 {
			wins++
			grossProfit += t.PnLMicros
		} else if t.PnLMicros < 0 {
			losses++
			grossLoss += -t.PnLMicros
		}
	}

	m.TotalPnL = totalPnL
	if initialCapital > 0 {
		m.TotalReturn = float64(totalPnL) / float64(initialCapital)
	}
	if len(trades) > 0 {
		m.WinRate = float64(wins) / float64(len(trades))
	}
	if grossLoss > 0 {
		m.ProfitFactor = float64(grossProfit) / float64(grossLoss)
	}

	m.MaxDrawdown = ComputeMaxDrawdown(equityCurve)
	m.SharpeRatio = ComputeSharpe(trades, initialCapital)

	return m
}

// ComputeMaxDrawdown returns the maximum peak-to-trough drawdown as a fraction.
func ComputeMaxDrawdown(equityCurve []int64) float64 {
	if len(equityCurve) < 2 {
		return 0
	}
	peak := equityCurve[0]
	maxDD := 0.0
	for _, eq := range equityCurve {
		if eq > peak {
			peak = eq
		}
		if peak > 0 {
			dd := float64(peak-eq) / float64(peak)
			if dd > maxDD {
				maxDD = dd
			}
		}
	}
	return maxDD
}

// ComputeSharpe computes an annualized Sharpe ratio from per-trade returns.
// Assumes ~252 trading days and averages trades per day.
func ComputeSharpe(trades []Trade, initialCapital int64) float64 {
	if len(trades) < 2 || initialCapital == 0 {
		return 0
	}

	returns := make([]float64, len(trades))
	for i, t := range trades {
		returns[i] = float64(t.PnLMicros) / float64(initialCapital)
	}

	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))

	variance := 0.0
	for _, r := range returns {
		d := r - mean
		variance += d * d
	}
	variance /= float64(len(returns) - 1)
	stddev := math.Sqrt(variance)

	if stddev == 0 {
		return 0
	}

	// Annualize assuming ~252 trades per year as a rough approximation
	return (mean / stddev) * math.Sqrt(252)
}
