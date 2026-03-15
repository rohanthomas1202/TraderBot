package risk

import (
	"context"
	"testing"
	"time"

	"autonomy-platform/internal/config"
	"autonomy-platform/internal/models"
)

// testPolicy returns a policy matching paper.yaml for deterministic testing.
func testPolicy() *config.Policy {
	return &config.Policy{
		Mode: "paper",
		AllowedMarkets: map[string][]string{
			"mock": {"*"},
		},
		PerTrade: config.PerTradeLimits{
			MaxNotionalMicros: 10_000_000_000, // $10,000
			MaxQuantity:       1000,
			MinPriceMicros:    10_000,  // $0.01
			MaxPriceMicros:    990_000, // $0.99
			MaxSpreadBps:      1000,    // 10%
		},
		PerPosition: config.PerPositionLimits{
			MaxNotionalMicros:   50_000_000_000, // $50,000
			MaxQuantity:         5000,
			MaxConcentrationPct: 25.0,
		},
		PerStrategy: config.PerStrategyLimits{
			MaxDailyLossMicros:     5_000_000_000, // $5,000
			MaxDailyTurnoverMicros: 50_000_000_000,
			MaxOrdersPerMinute:     10,
			MaxConsecutiveLosses:   10,
			MaxOpenOrders:          20,
		},
		PerVenue: map[string]config.VenueLimits{
			"mock": {
				MaxExposureMicros:  100_000_000_000, // $100,000
				MaxDailyLossMicros: 50_000_000_000,
			},
		},
		Global: config.GlobalLimits{
			MaxTotalExposureMicros: 100_000_000_000, // $100,000
			MaxDailyLossMicros:     50_000_000_000,
			MaxDrawdownPct:         15.0,
		},
		DataQuality: config.DataQuality{
			MaxDataAgeSeconds: 5,
			MinOrderbookDepth: 1,
		},
	}
}

// testState returns a clean state with fresh market data pre-loaded.
func testState() *State {
	s := newEmptyState()
	s.MarketData["mock:TEST-MARKET"] = &models.MarketData{
		Venue:          "mock",
		MarketID:       "TEST-MARKET",
		BidPriceMicros: 400_000,
		AskPriceMicros: 410_000,
		BidDepth:       5,
		AskDepth:       5,
		UpdatedAt:      time.Now(),
	}
	return s
}

// validOrder returns an order that passes all 20 checks against testPolicy/testState.
func validOrder() *models.ProposedOrder {
	return &models.ProposedOrder{
		TraceID:        "test-trace-001",
		StrategyID:     "test-strategy",
		Venue:          "mock",
		MarketID:       "TEST-MARKET",
		Side:           models.SideBuy,
		Quantity:       10,
		PriceMicros:    400_000,
		NotionalMicros: 4_000_000, // 10 * 400_000
		ProposedAt:     time.Now(),
	}
}

// --- Check 1: system_mode ---

func TestCheck_SystemMode_Normal(t *testing.T) {
	r := checkSystemMode(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass in normal mode, got: %s", r.Detail)
	}
}

func TestCheck_SystemMode_SoftPause(t *testing.T) {
	s := testState()
	s.SystemMode = "soft_pause"
	r := checkSystemMode(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail in soft_pause mode")
	}
}

func TestCheck_SystemMode_CancelOnly(t *testing.T) {
	s := testState()
	s.SystemMode = "cancel_only"
	r := checkSystemMode(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail in cancel_only mode")
	}
}

func TestCheck_SystemMode_FullStop(t *testing.T) {
	s := testState()
	s.SystemMode = "full_stop"
	r := checkSystemMode(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail in full_stop mode")
	}
}

// --- Check 2: strategy_halted ---

func TestCheck_StrategyNotHalted_Pass(t *testing.T) {
	r := checkStrategyNotHalted(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_StrategyHalted_Fail(t *testing.T) {
	s := testState()
	s.Strategies["test-strategy"] = &StrategyState{Halted: true, HaltReason: "manual halt"}
	r := checkStrategyNotHalted(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail when strategy is halted")
	}
}

// --- Check 3: venue_halted ---

func TestCheck_VenueNotHalted_Pass(t *testing.T) {
	r := checkVenueNotHalted(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_VenueHalted_Fail(t *testing.T) {
	s := testState()
	s.Venues["mock"] = &VenueState{Halted: true}
	r := checkVenueNotHalted(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail when venue is halted")
	}
}

// --- Check 4: market_allowed ---

func TestCheck_MarketAllowed_Wildcard(t *testing.T) {
	r := checkMarketAllowed(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass with wildcard, got: %s", r.Detail)
	}
}

func TestCheck_MarketAllowed_UnknownVenue(t *testing.T) {
	o := validOrder()
	o.Venue = "unknown-venue"
	r := checkMarketAllowed(context.Background(), o, testState(), testPolicy())
	if r.Passed {
		t.Fatal("expected fail for unknown venue")
	}
}

func TestCheck_MarketAllowed_GlobPattern(t *testing.T) {
	p := testPolicy()
	p.AllowedMarkets["mock"] = []string{"TEST-*"}
	r := checkMarketAllowed(context.Background(), validOrder(), testState(), p)
	if !r.Passed {
		t.Fatalf("expected pass with glob pattern, got: %s", r.Detail)
	}
}

func TestCheck_MarketAllowed_NoMatch(t *testing.T) {
	p := testPolicy()
	p.AllowedMarkets["mock"] = []string{"OTHER-*"}
	r := checkMarketAllowed(context.Background(), validOrder(), testState(), p)
	if r.Passed {
		t.Fatal("expected fail when market doesn't match pattern")
	}
}

// --- Check 5: data_freshness ---

func TestCheck_DataFreshness_Fresh(t *testing.T) {
	r := checkDataFreshness(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass with fresh data, got: %s", r.Detail)
	}
}

func TestCheck_DataFreshness_Stale(t *testing.T) {
	s := testState()
	s.MarketData["mock:TEST-MARKET"].UpdatedAt = time.Now().Add(-10 * time.Second)
	r := checkDataFreshness(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail with stale data")
	}
}

func TestCheck_DataFreshness_NoData(t *testing.T) {
	s := newEmptyState()
	r := checkDataFreshness(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail with no market data")
	}
}

// --- Check 6: price_sanity ---

func TestCheck_PriceSanity_Valid(t *testing.T) {
	r := checkPriceSanity(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_PriceSanity_TooLow(t *testing.T) {
	o := validOrder()
	o.PriceMicros = 5_000 // below $0.01 min
	o.NotionalMicros = int64(o.Quantity) * o.PriceMicros
	r := checkPriceSanity(context.Background(), o, testState(), testPolicy())
	if r.Passed {
		t.Fatal("expected fail for price below minimum")
	}
}

func TestCheck_PriceSanity_TooHigh(t *testing.T) {
	o := validOrder()
	o.PriceMicros = 995_000 // above $0.99 max
	o.NotionalMicros = int64(o.Quantity) * o.PriceMicros
	r := checkPriceSanity(context.Background(), o, testState(), testPolicy())
	if r.Passed {
		t.Fatal("expected fail for price above maximum")
	}
}

// --- Check 7: spread_check ---

func TestCheck_Spread_Narrow(t *testing.T) {
	r := checkSpread(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass with narrow spread, got: %s", r.Detail)
	}
}

func TestCheck_Spread_TooWide(t *testing.T) {
	s := testState()
	// Set a wide spread: bid=100k, ask=300k → spread ~100% of mid
	s.MarketData["mock:TEST-MARKET"].BidPriceMicros = 100_000
	s.MarketData["mock:TEST-MARKET"].AskPriceMicros = 300_000
	r := checkSpread(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail with wide spread")
	}
}

func TestCheck_Spread_NoData(t *testing.T) {
	s := newEmptyState()
	r := checkSpread(context.Background(), validOrder(), s, testPolicy())
	if !r.Passed {
		t.Fatal("expected pass (skip) with no market data")
	}
}

// --- Check 8: per_trade_size ---

func TestCheck_PerTradeSize_WithinLimit(t *testing.T) {
	r := checkPerTradeSize(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_PerTradeSize_ExceedsLimit(t *testing.T) {
	o := validOrder()
	o.Quantity = 2000 // exceeds 1000 limit
	r := checkPerTradeSize(context.Background(), o, testState(), testPolicy())
	if r.Passed {
		t.Fatal("expected fail for quantity exceeding limit")
	}
}

// --- Check 9: per_trade_notional ---

func TestCheck_PerTradeNotional_WithinLimit(t *testing.T) {
	r := checkPerTradeNotional(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_PerTradeNotional_ExceedsLimit(t *testing.T) {
	o := validOrder()
	o.NotionalMicros = 20_000_000_000 // $20k, exceeds $10k limit
	r := checkPerTradeNotional(context.Background(), o, testState(), testPolicy())
	if r.Passed {
		t.Fatal("expected fail for notional exceeding limit")
	}
}

// --- Check 10: position_limit ---

func TestCheck_PositionLimit_NoPrior(t *testing.T) {
	r := checkPositionLimit(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass with no prior position, got: %s", r.Detail)
	}
}

func TestCheck_PositionLimit_ExceedsLimit(t *testing.T) {
	s := testState()
	key := "mock:TEST-MARKET:test-strategy"
	s.Markets[key] = &MarketState{
		PositionNotional: models.Money(49_999_000_000), // just under $50k
	}
	o := validOrder()
	o.NotionalMicros = 5_000_000_000 // would push over $50k limit
	r := checkPositionLimit(context.Background(), o, s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail for position exceeding limit")
	}
}

// --- Check 11: concentration ---

func TestCheck_Concentration_NoExposure(t *testing.T) {
	r := checkConcentration(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass with zero exposure, got: %s", r.Detail)
	}
}

func TestCheck_Concentration_WithinLimit(t *testing.T) {
	s := testState()
	s.TotalExposure = models.Money(100_000_000_000) // $100k total
	// Add positions across multiple markets so this one is small
	s.Markets["mock:OTHER-1:test-strategy"] = &MarketState{PositionNotional: 50_000_000_000}
	s.Markets["mock:OTHER-2:test-strategy"] = &MarketState{PositionNotional: 50_000_000_000}
	r := checkConcentration(context.Background(), validOrder(), s, testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_Concentration_ExceedsLimit(t *testing.T) {
	s := testState()
	s.TotalExposure = models.Money(10_000_000) // $10 total
	key := "mock:TEST-MARKET:test-strategy"
	s.Markets[key] = &MarketState{PositionNotional: 5_000_000} // $5 existing
	o := validOrder()
	o.NotionalMicros = 9_000_000 // would be ~74% concentration
	r := checkConcentration(context.Background(), o, s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail for high concentration")
	}
}

// --- Check 12: strategy_daily_loss ---

func TestCheck_StrategyDailyLoss_NoHistory(t *testing.T) {
	r := checkStrategyDailyLoss(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass with no history, got: %s", r.Detail)
	}
}

func TestCheck_StrategyDailyLoss_WithinLimit(t *testing.T) {
	s := testState()
	s.Strategies["test-strategy"] = &StrategyState{DailyPnL: models.Money(-1_000_000_000)} // -$1k
	r := checkStrategyDailyLoss(context.Background(), validOrder(), s, testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_StrategyDailyLoss_ExceedsLimit(t *testing.T) {
	s := testState()
	s.Strategies["test-strategy"] = &StrategyState{DailyPnL: models.Money(-6_000_000_000)} // -$6k > $5k limit
	r := checkStrategyDailyLoss(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail for exceeding daily loss limit")
	}
}

// --- Check 13: strategy_order_freq ---

func TestCheck_StrategyOrderFreq_NoHistory(t *testing.T) {
	r := checkStrategyOrderFrequency(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass with no history, got: %s", r.Detail)
	}
}

func TestCheck_StrategyOrderFreq_WithinLimit(t *testing.T) {
	s := testState()
	// 5 orders in last minute, limit is 10
	now := time.Now()
	for i := 0; i < 5; i++ {
		s.RecentOrderTimes["test-strategy"] = append(s.RecentOrderTimes["test-strategy"], now.Add(-time.Duration(i)*5*time.Second))
	}
	r := checkStrategyOrderFrequency(context.Background(), validOrder(), s, testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_StrategyOrderFreq_ExceedsLimit(t *testing.T) {
	s := testState()
	now := time.Now()
	for i := 0; i < 10; i++ {
		s.RecentOrderTimes["test-strategy"] = append(s.RecentOrderTimes["test-strategy"], now.Add(-time.Duration(i)*time.Second))
	}
	r := checkStrategyOrderFrequency(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail for exceeding order frequency")
	}
}

// --- Check 14: strategy_consec_loss ---

func TestCheck_StrategyConsecLoss_NoHistory(t *testing.T) {
	r := checkStrategyConsecutiveLosses(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass with no history, got: %s", r.Detail)
	}
}

func TestCheck_StrategyConsecLoss_WithinLimit(t *testing.T) {
	s := testState()
	s.Strategies["test-strategy"] = &StrategyState{ConsecutiveLosses: 5}
	r := checkStrategyConsecutiveLosses(context.Background(), validOrder(), s, testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_StrategyConsecLoss_ExceedsLimit(t *testing.T) {
	s := testState()
	s.Strategies["test-strategy"] = &StrategyState{ConsecutiveLosses: 10} // equals limit
	r := checkStrategyConsecutiveLosses(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail at consecutive loss limit")
	}
}

// --- Check 15: venue_exposure ---

func TestCheck_VenueExposure_WithinLimit(t *testing.T) {
	r := checkVenueExposure(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_VenueExposure_ExceedsLimit(t *testing.T) {
	s := testState()
	s.Venues["mock"] = &VenueState{Exposure: models.Money(99_999_000_000)}
	o := validOrder()
	o.NotionalMicros = 5_000_000_000 // would push over $100k limit
	r := checkVenueExposure(context.Background(), o, s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail for venue exposure exceeding limit")
	}
}

func TestCheck_VenueExposure_NoVenueConfig(t *testing.T) {
	o := validOrder()
	o.Venue = "unconfigured-venue"
	p := testPolicy()
	r := checkVenueExposure(context.Background(), o, testState(), p)
	if !r.Passed {
		t.Fatalf("expected pass when no venue config, got: %s", r.Detail)
	}
}

// --- Check 16: global_exposure ---

func TestCheck_GlobalExposure_WithinLimit(t *testing.T) {
	r := checkGlobalExposure(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_GlobalExposure_ExceedsLimit(t *testing.T) {
	s := testState()
	s.TotalExposure = models.Money(99_999_000_000)
	o := validOrder()
	o.NotionalMicros = 5_000_000_000
	r := checkGlobalExposure(context.Background(), o, s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail for global exposure exceeding limit")
	}
}

// --- Check 17: global_daily_loss ---

func TestCheck_GlobalDailyLoss_WithinLimit(t *testing.T) {
	r := checkGlobalDailyLoss(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_GlobalDailyLoss_ExceedsLimit(t *testing.T) {
	s := testState()
	s.DailyPnL = models.Money(-51_000_000_000) // -$51k, exceeds $50k limit
	r := checkGlobalDailyLoss(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail for daily loss exceeding limit")
	}
}

// --- Check 18: global_drawdown ---

func TestCheck_GlobalDrawdown_NoPeak(t *testing.T) {
	r := checkGlobalDrawdown(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass with zero peak, got: %s", r.Detail)
	}
}

func TestCheck_GlobalDrawdown_WithinLimit(t *testing.T) {
	s := testState()
	s.PeakEquity = models.Money(100_000_000_000)    // $100k peak
	s.CurrentEquity = models.Money(90_000_000_000)   // $90k current → 10% drawdown
	r := checkGlobalDrawdown(context.Background(), validOrder(), s, testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass at 10%% drawdown (limit 15%%), got: %s", r.Detail)
	}
}

func TestCheck_GlobalDrawdown_ExceedsLimit(t *testing.T) {
	s := testState()
	s.PeakEquity = models.Money(100_000_000_000)    // $100k peak
	s.CurrentEquity = models.Money(84_000_000_000)   // $84k → 16% drawdown
	r := checkGlobalDrawdown(context.Background(), validOrder(), s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail for drawdown exceeding 15% limit")
	}
}

// --- Check 19: duplicate_order ---

func TestCheck_DuplicateOrder_NoPrior(t *testing.T) {
	r := checkDuplicateOrder(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass with no prior orders, got: %s", r.Detail)
	}
}

func TestCheck_DuplicateOrder_Recent(t *testing.T) {
	s := testState()
	o := validOrder()
	s.RecentOrderKeys[o.IdempotencyKey()] = time.Now().Add(-5 * time.Second) // 5s ago
	r := checkDuplicateOrder(context.Background(), o, s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail for duplicate within 30s window")
	}
}

func TestCheck_DuplicateOrder_Expired(t *testing.T) {
	s := testState()
	o := validOrder()
	s.RecentOrderKeys[o.IdempotencyKey()] = time.Now().Add(-60 * time.Second) // 60s ago
	r := checkDuplicateOrder(context.Background(), o, s, testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass for duplicate outside 30s window, got: %s", r.Detail)
	}
}

// --- Check 20: fat_finger ---

func TestCheck_FatFinger_NoHistory(t *testing.T) {
	r := checkFatFinger(context.Background(), validOrder(), testState(), testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass with insufficient history, got: %s", r.Detail)
	}
}

func TestCheck_FatFinger_WithinRange(t *testing.T) {
	s := testState()
	// Recent notionals around $4 each
	s.RecentNotionals["test-strategy"] = []int64{4_000_000, 4_000_000, 4_000_000, 4_000_000, 4_000_000}
	o := validOrder()
	o.NotionalMicros = 10_000_000 // $10, ~2.5x average — within 5x
	r := checkFatFinger(context.Background(), o, s, testPolicy())
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Detail)
	}
}

func TestCheck_FatFinger_TooLarge(t *testing.T) {
	s := testState()
	// Recent notionals around $1 each
	s.RecentNotionals["test-strategy"] = []int64{1_000_000, 1_000_000, 1_000_000, 1_000_000, 1_000_000}
	o := validOrder()
	o.NotionalMicros = 6_000_000 // $6, >5x the $1 average
	r := checkFatFinger(context.Background(), o, s, testPolicy())
	if r.Passed {
		t.Fatal("expected fail for fat finger >5x average")
	}
}

// --- RunAllChecks integration ---

func TestRunAllChecks_ValidOrder_AllPass(t *testing.T) {
	results := RunAllChecks(context.Background(), validOrder(), testState(), testPolicy())
	if len(results) != 20 {
		t.Fatalf("expected 20 checks, got %d", len(results))
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("check %s failed: %s", r.Name, r.Detail)
		}
	}
}

func TestRunAllChecks_MultipleFailures_NoShortCircuit(t *testing.T) {
	s := testState()
	s.SystemMode = "full_stop" // check 1 fails
	s.MarketData["mock:TEST-MARKET"].UpdatedAt = time.Now().Add(-10 * time.Second) // check 5 fails

	o := validOrder()
	o.NotionalMicros = 20_000_000_000 // check 9 fails
	o.Quantity = 2000                 // check 8 fails

	results := RunAllChecks(context.Background(), o, s, testPolicy())

	// All 20 checks must run (no short-circuit)
	if len(results) != 20 {
		t.Fatalf("expected 20 checks, got %d", len(results))
	}

	// Collect failures
	failures := make(map[string]bool)
	for _, r := range results {
		if !r.Passed {
			failures[r.Name] = true
		}
	}

	expected := []string{"system_mode", "data_freshness", "per_trade_size", "per_trade_notional"}
	for _, name := range expected {
		if !failures[name] {
			t.Errorf("expected %s to fail", name)
		}
	}
	t.Logf("Total failures: %d (expected at least %d)", len(failures), len(expected))
}
