//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/risk"
)

// TestDeniedOrder_ExceedsNotionalLimit verifies that orders exceeding
// per-trade notional limits are denied with the correct check failure.
func TestDeniedOrder_ExceedsNotionalLimit(t *testing.T) {
	db, _, pub := setupTestEnv(t)
	ctx := context.Background()
	policy := loadTestPolicy(t)
	hmacKey := []byte("test-hmac-key")

	auditor := audit.NewLogger("test", db)
	riskEngine := risk.NewEngine(db, pub, auditor, policy, hmacKey)
	riskEngine.LoadState(ctx)

	// Inject market data
	riskEngine.UpdateMarketData(&models.MarketData{
		Venue:           "mock",
		MarketID:        "MOCK-HUGE-ORDER",
		BidPriceMicros:  500_000,
		AskPriceMicros:  510_000,
		LastPriceMicros: 505_000,
		BidDepth:        5,
		AskDepth:        5,
		UpdatedAt:       time.Now(),
	})

	// Create order that exceeds per-trade notional limit ($10,000 in paper.yaml)
	order := &models.ProposedOrder{
		TraceID:        "test-denied-001",
		StrategyID:     "test-strategy",
		Venue:          "mock",
		MarketID:       "MOCK-HUGE-ORDER",
		Side:           models.SideBuy,
		Quantity:       100000,
		PriceMicros:    500_000,
		NotionalMicros: 50_000_000_000, // $50,000 — exceeds $10,000 limit
		ProposedAt:     time.Now(),
	}

	approval, err := riskEngine.EvaluateOrder(ctx, order)
	if err != nil {
		t.Fatalf("evaluate order: %v", err)
	}

	if approval.Decision != risk.DecisionDenied {
		t.Fatalf("expected denied, got %s", approval.Decision)
	}

	// Verify the per_trade_notional check failed
	found := false
	for _, c := range approval.Checks {
		if c.Name == "per_trade_notional" && !c.Passed {
			found = true
			t.Logf("Correctly denied: %s — %s", c.Name, c.Detail)
		}
	}
	if !found {
		t.Error("expected per_trade_notional check to fail")
	}

	// Verify denial is persisted in risk.policy_decisions
	var decision string
	db.QueryRow(ctx,
		`SELECT decision FROM risk.policy_decisions WHERE trace_id = $1`,
		order.TraceID).Scan(&decision)
	if decision != "denied" {
		t.Errorf("expected 'denied' in DB, got %s", decision)
	}
}

// TestDeniedOrder_MarketNotAllowed verifies orders to unlisted markets are denied.
func TestDeniedOrder_MarketNotAllowed(t *testing.T) {
	db, _, pub := setupTestEnv(t)
	ctx := context.Background()
	policy := loadTestPolicy(t)
	hmacKey := []byte("test-hmac-key")

	auditor := audit.NewLogger("test", db)
	riskEngine := risk.NewEngine(db, pub, auditor, policy, hmacKey)
	riskEngine.LoadState(ctx)

	riskEngine.UpdateMarketData(&models.MarketData{
		Venue:           "unknown-venue",
		MarketID:        "UNKNOWN-MARKET",
		BidPriceMicros:  500_000,
		AskPriceMicros:  510_000,
		UpdatedAt:       time.Now(),
	})

	order := &models.ProposedOrder{
		TraceID:        "test-denied-market-001",
		StrategyID:     "test-strategy",
		Venue:          "unknown-venue", // not in policy allowed_markets
		MarketID:       "UNKNOWN-MARKET",
		Side:           models.SideBuy,
		Quantity:       1,
		PriceMicros:    500_000,
		NotionalMicros: 500_000,
		ProposedAt:     time.Now(),
	}

	approval, err := riskEngine.EvaluateOrder(ctx, order)
	if err != nil {
		t.Fatalf("evaluate order: %v", err)
	}

	if approval.Decision != risk.DecisionDenied {
		t.Fatalf("expected denied, got %s", approval.Decision)
	}

	found := false
	for _, c := range approval.Checks {
		if c.Name == "market_allowed" && !c.Passed {
			found = true
			t.Logf("Correctly denied: %s — %s", c.Name, c.Detail)
		}
	}
	if !found {
		t.Error("expected market_allowed check to fail")
	}
}

// TestDeniedOrder_StaleData verifies orders with stale market data are denied.
func TestDeniedOrder_StaleData(t *testing.T) {
	db, _, pub := setupTestEnv(t)
	ctx := context.Background()
	policy := loadTestPolicy(t)
	hmacKey := []byte("test-hmac-key")

	auditor := audit.NewLogger("test", db)
	riskEngine := risk.NewEngine(db, pub, auditor, policy, hmacKey)
	riskEngine.LoadState(ctx)

	// Inject stale market data (10 seconds old, limit is 5)
	riskEngine.UpdateMarketData(&models.MarketData{
		Venue:           "mock",
		MarketID:        "MOCK-STALE-TEST",
		BidPriceMicros:  400_000,
		AskPriceMicros:  410_000,
		UpdatedAt:       time.Now().Add(-10 * time.Second), // stale!
	})

	order := &models.ProposedOrder{
		TraceID:        "test-stale-001",
		StrategyID:     "test-strategy",
		Venue:          "mock",
		MarketID:       "MOCK-STALE-TEST",
		Side:           models.SideBuy,
		Quantity:       1,
		PriceMicros:    400_000,
		NotionalMicros: 400_000,
		ProposedAt:     time.Now(),
	}

	approval, err := riskEngine.EvaluateOrder(ctx, order)
	if err != nil {
		t.Fatalf("evaluate order: %v", err)
	}

	if approval.Decision != risk.DecisionDenied {
		t.Fatalf("expected denied, got %s", approval.Decision)
	}

	found := false
	for _, c := range approval.Checks {
		if c.Name == "data_freshness" && !c.Passed {
			found = true
			t.Logf("Correctly denied: %s — %s", c.Name, c.Detail)
		}
	}
	if !found {
		t.Error("expected data_freshness check to fail")
	}
}
