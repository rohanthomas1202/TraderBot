package strategy

import (
	"testing"

	"autonomy-platform/internal/models"
)

func TestSimpleMomentum_BuyBelowThreshold(t *testing.T) {
	fn := SimpleMomentum()

	data := map[string]*models.MarketData{
		"mock:MOCK-CHEAP": {
			Venue:          "mock",
			MarketID:       "MOCK-CHEAP",
			BidPriceMicros: 300_000, // 30¢
			AskPriceMicros: 320_000, // 32¢ → mid = 310_000 (31¢)
		},
	}

	signals := fn(data)
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}
	if signals[0].Side != models.SideBuy {
		t.Errorf("expected buy signal, got %s", signals[0].Side)
	}
	if signals[0].MarketID != "MOCK-CHEAP" {
		t.Errorf("expected market MOCK-CHEAP, got %s", signals[0].MarketID)
	}
	if signals[0].Quantity != 1 {
		t.Errorf("expected quantity 1, got %d", signals[0].Quantity)
	}
	expectedMid := int64(310_000)
	if signals[0].PriceMicros != expectedMid {
		t.Errorf("expected price %d, got %d", expectedMid, signals[0].PriceMicros)
	}
}

func TestSimpleMomentum_SellAboveThreshold(t *testing.T) {
	fn := SimpleMomentum()

	data := map[string]*models.MarketData{
		"mock:MOCK-EXPENSIVE": {
			Venue:          "mock",
			MarketID:       "MOCK-EXPENSIVE",
			BidPriceMicros: 700_000, // 70¢
			AskPriceMicros: 720_000, // 72¢ → mid = 710_000 (71¢)
		},
	}

	signals := fn(data)
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}
	if signals[0].Side != models.SideSell {
		t.Errorf("expected sell signal, got %s", signals[0].Side)
	}
	if signals[0].MarketID != "MOCK-EXPENSIVE" {
		t.Errorf("expected market MOCK-EXPENSIVE, got %s", signals[0].MarketID)
	}
}

func TestSimpleMomentum_NoSignalInDeadZone(t *testing.T) {
	fn := SimpleMomentum()

	data := map[string]*models.MarketData{
		"mock:MOCK-MIDDLE": {
			Venue:          "mock",
			MarketID:       "MOCK-MIDDLE",
			BidPriceMicros: 490_000, // 49¢
			AskPriceMicros: 510_000, // 51¢ → mid = 500_000 (50¢)
		},
	}

	signals := fn(data)
	if len(signals) != 0 {
		t.Errorf("expected no signals in dead zone (40¢-60¢), got %d", len(signals))
	}
}

func TestSimpleMomentum_BoundaryAt40Cents(t *testing.T) {
	fn := SimpleMomentum()

	// Exactly at 40¢ — mid == 400_000, should NOT trigger buy (< 400_000 required)
	data := map[string]*models.MarketData{
		"mock:MOCK-BOUNDARY": {
			Venue:          "mock",
			MarketID:       "MOCK-BOUNDARY",
			BidPriceMicros: 390_000,
			AskPriceMicros: 410_000, // mid = 400_000
		},
	}

	signals := fn(data)
	if len(signals) != 0 {
		t.Errorf("expected no signal at exactly 40¢ boundary, got %d", len(signals))
	}
}

func TestSimpleMomentum_BoundaryAt60Cents(t *testing.T) {
	fn := SimpleMomentum()

	// Exactly at 60¢ — mid == 600_000, should NOT trigger sell (> 600_000 required)
	data := map[string]*models.MarketData{
		"mock:MOCK-BOUNDARY": {
			Venue:          "mock",
			MarketID:       "MOCK-BOUNDARY",
			BidPriceMicros: 590_000,
			AskPriceMicros: 610_000, // mid = 600_000
		},
	}

	signals := fn(data)
	if len(signals) != 0 {
		t.Errorf("expected no signal at exactly 60¢ boundary, got %d", len(signals))
	}
}

func TestSimpleMomentum_MultipleMarkets(t *testing.T) {
	fn := SimpleMomentum()

	data := map[string]*models.MarketData{
		"mock:CHEAP": {
			Venue:          "mock",
			MarketID:       "CHEAP",
			BidPriceMicros: 200_000,
			AskPriceMicros: 220_000, // mid = 210_000 → buy
		},
		"mock:MIDDLE": {
			Venue:          "mock",
			MarketID:       "MIDDLE",
			BidPriceMicros: 490_000,
			AskPriceMicros: 510_000, // mid = 500_000 → no signal
		},
		"mock:EXPENSIVE": {
			Venue:          "mock",
			MarketID:       "EXPENSIVE",
			BidPriceMicros: 800_000,
			AskPriceMicros: 820_000, // mid = 810_000 → sell
		},
	}

	signals := fn(data)
	if len(signals) != 2 {
		t.Fatalf("expected 2 signals (1 buy, 1 sell), got %d", len(signals))
	}

	var hasBuy, hasSell bool
	for _, s := range signals {
		if s.Side == models.SideBuy {
			hasBuy = true
		}
		if s.Side == models.SideSell {
			hasSell = true
		}
	}
	if !hasBuy {
		t.Error("expected a buy signal for cheap market")
	}
	if !hasSell {
		t.Error("expected a sell signal for expensive market")
	}
}

func TestSimpleMomentum_ZeroPriceSkipped(t *testing.T) {
	fn := SimpleMomentum()

	data := map[string]*models.MarketData{
		"mock:ZERO": {
			Venue:          "mock",
			MarketID:       "ZERO",
			BidPriceMicros: 0,
			AskPriceMicros: 0, // mid = 0 → skip
		},
	}

	signals := fn(data)
	if len(signals) != 0 {
		t.Errorf("expected no signals for zero-price market, got %d", len(signals))
	}
}

func TestSimpleMomentum_EmptyData(t *testing.T) {
	fn := SimpleMomentum()

	signals := fn(nil)
	if len(signals) != 0 {
		t.Errorf("expected no signals for nil data, got %d", len(signals))
	}

	signals = fn(map[string]*models.MarketData{})
	if len(signals) != 0 {
		t.Errorf("expected no signals for empty data, got %d", len(signals))
	}
}
