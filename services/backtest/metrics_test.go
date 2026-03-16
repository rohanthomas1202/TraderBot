package backtest

import (
	"math"
	"testing"
)

func TestComputeMaxDrawdown_Basic(t *testing.T) {
	curve := []int64{100, 110, 105, 120, 90, 130}
	dd := ComputeMaxDrawdown(curve)
	// Peak was 120, trough was 90 → 30/120 = 0.25
	expected := 0.25
	if math.Abs(dd-expected) > 0.001 {
		t.Errorf("expected drawdown %.4f, got %.4f", expected, dd)
	}
}

func TestComputeMaxDrawdown_Empty(t *testing.T) {
	dd := ComputeMaxDrawdown(nil)
	if dd != 0 {
		t.Errorf("expected 0, got %f", dd)
	}
}

func TestComputeMaxDrawdown_MonotonicUp(t *testing.T) {
	curve := []int64{100, 110, 120, 130}
	dd := ComputeMaxDrawdown(curve)
	if dd != 0 {
		t.Errorf("expected 0 drawdown for monotonically increasing, got %f", dd)
	}
}

func TestComputeFromTrades_ZeroTrades(t *testing.T) {
	m := ComputeFromTrades(nil, nil, 100_000_000)
	if m.TradeCount != 0 || m.TotalReturn != 0 {
		t.Errorf("expected zero metrics for no trades, got %+v", m)
	}
}

func TestComputeFromTrades_AllWins(t *testing.T) {
	trades := []Trade{
		{PnLMicros: 1_000_000},
		{PnLMicros: 2_000_000},
		{PnLMicros: 500_000},
	}
	curve := []int64{100_000_000, 101_000_000, 103_000_000, 103_500_000}
	m := ComputeFromTrades(trades, curve, 100_000_000)

	if m.WinRate != 1.0 {
		t.Errorf("expected 100%% win rate, got %f", m.WinRate)
	}
	if m.TotalPnL != 3_500_000 {
		t.Errorf("expected total PnL 3500000, got %d", m.TotalPnL)
	}
	if m.ProfitFactor != 0 {
		// No losses, so profit factor denominator is 0 → 0
	}
}

func TestComputeFromTrades_AllLosses(t *testing.T) {
	trades := []Trade{
		{PnLMicros: -1_000_000},
		{PnLMicros: -500_000},
	}
	curve := []int64{100_000_000, 99_000_000, 98_500_000}
	m := ComputeFromTrades(trades, curve, 100_000_000)

	if m.WinRate != 0 {
		t.Errorf("expected 0%% win rate, got %f", m.WinRate)
	}
	if m.ProfitFactor != 0 {
		t.Errorf("expected profit factor 0, got %f", m.ProfitFactor)
	}
}

func TestComputeFromTrades_Mixed(t *testing.T) {
	trades := []Trade{
		{PnLMicros: 2_000_000},
		{PnLMicros: -1_000_000},
		{PnLMicros: 3_000_000},
		{PnLMicros: -500_000},
	}
	curve := []int64{100_000_000, 102_000_000, 101_000_000, 104_000_000, 103_500_000}
	m := ComputeFromTrades(trades, curve, 100_000_000)

	if m.TradeCount != 4 {
		t.Errorf("expected 4 trades, got %d", m.TradeCount)
	}
	if m.WinRate != 0.5 {
		t.Errorf("expected 50%% win rate, got %f", m.WinRate)
	}
	// Profit factor: 5M / 1.5M = 3.333
	expected := 5_000_000.0 / 1_500_000.0
	if math.Abs(m.ProfitFactor-expected) > 0.01 {
		t.Errorf("expected profit factor %.4f, got %.4f", expected, m.ProfitFactor)
	}
}

func TestComputeSharpe_FewTrades(t *testing.T) {
	// Less than 2 trades → 0
	sharpe := ComputeSharpe([]Trade{{PnLMicros: 100}}, 100_000_000)
	if sharpe != 0 {
		t.Errorf("expected 0, got %f", sharpe)
	}
}
