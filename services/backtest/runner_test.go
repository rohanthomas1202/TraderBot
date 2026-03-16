package backtest

import (
	"context"
	"testing"
	"time"

	"autonomy-platform/internal/config"
	"autonomy-platform/internal/models"
)

func testPolicy() *config.Policy {
	return &config.Policy{
		Mode: "paper",
		AllowedMarkets: map[string][]string{
			"mock": {"*"},
		},
		PerTrade: config.PerTradeLimits{
			MaxQuantity:       100,
			MaxNotionalMicros: 50_000_000,
			MinPriceMicros:    10_000,
			MaxPriceMicros:    990_000,
			MaxSpreadBps:      5000,
		},
		PerPosition: config.PerPositionLimits{
			MaxNotionalMicros:   100_000_000,
			MaxConcentrationPct: 100,
		},
		PerStrategy: config.PerStrategyLimits{
			MaxDailyLossMicros:   50_000_000,
			MaxOrdersPerMinute:   60,
			MaxConsecutiveLosses: 20,
		},
		Global: config.GlobalLimits{
			MaxTotalExposureMicros: 500_000_000,
			MaxDailyLossMicros:     100_000_000,
			MaxDrawdownPct:         50,
		},
		DataQuality: config.DataQuality{
			MaxDataAgeSeconds: 3600,
		},
	}
}

func TestRun_WithSyntheticData(t *testing.T) {
	now := time.Now().UTC()

	// Create synthetic ticks: alternating cheap and expensive prices
	// to trigger SimpleMomentum buy/sell signals
	var ticks []Tick
	for i := 0; i < 20; i++ {
		ts := now.Add(time.Duration(i) * time.Minute)
		var bid, ask int64
		if i%2 == 0 {
			bid, ask = 300_000, 300_000 // cheap, zero spread → buy signal at mid=300k, ask=300k fills
		} else {
			bid, ask = 700_000, 700_000 // expensive, zero spread → sell signal at mid=700k, bid=700k fills
		}
		ticks = append(ticks, Tick{
			Timestamp: ts,
			Data: map[string]*models.MarketData{
				"mock:TEST-MKT": {
					Venue:         "mock",
					MarketID:      "TEST-MKT",
					BidPriceMicros: bid,
					AskPriceMicros: ask,
					Volume24h:     1000,
					UpdatedAt:     ts,
				},
			},
		})
	}

	result, err := Run(context.Background(), RunConfig{
		StrategyName:   "simple-momentum",
		Venue:          "mock",
		Policy:         testPolicy(),
		InitialCapital: 100_000_000_000, // $100k
		FillMode:       "deterministic",
		Ticks:          ticks,
	})
	if err != nil {
		t.Fatalf("backtest run failed: %v", err)
	}

	if result.Metrics.TradeCount == 0 {
		t.Error("expected at least some trades")
	}
	if len(result.EquityCurve) < 2 {
		t.Error("expected equity curve with multiple points")
	}

	t.Logf("Trades: %d, P&L: $%.2f, Sharpe: %.4f, MaxDD: %.4f%%",
		result.Metrics.TradeCount,
		float64(result.Metrics.TotalPnL)/1e6,
		result.Metrics.SharpeRatio,
		result.Metrics.MaxDrawdown*100)
}

func TestRun_EmptyTicks(t *testing.T) {
	result, err := Run(context.Background(), RunConfig{
		StrategyName:   "simple-momentum",
		Venue:          "mock",
		Policy:         testPolicy(),
		InitialCapital: 100_000_000_000,
		FillMode:       "deterministic",
		Ticks:          nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Metrics.TradeCount != 0 {
		t.Errorf("expected 0 trades with no ticks, got %d", result.Metrics.TradeCount)
	}
}

func TestRun_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := Run(ctx, RunConfig{
		StrategyName:   "simple-momentum",
		Venue:          "mock",
		Policy:         testPolicy(),
		InitialCapital: 100_000_000_000,
		FillMode:       "deterministic",
		Ticks: []Tick{
			{Timestamp: time.Now(), Data: map[string]*models.MarketData{
				"mock:X": {Venue: "mock", MarketID: "X", BidPriceMicros: 300_000, AskPriceMicros: 350_000, UpdatedAt: time.Now()},
			}},
		},
	})
	if err == nil {
		t.Error("expected context cancellation error")
	}
}
