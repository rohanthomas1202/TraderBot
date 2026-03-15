//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/risk"

	"github.com/google/uuid"
)

// TestDuplicateOrder_SecondProposalDenied verifies that submitting the same
// order twice within the dedup window is caught by the duplicate_order check.
func TestDuplicateOrder_SecondProposalDenied(t *testing.T) {
	db, _, pub := setupTestEnv(t)
	ctx := context.Background()
	policy := loadTestPolicy(t)
	hmacKey := []byte("test-hmac-key")

	auditor := audit.NewLogger("test", db)
	riskEngine := risk.NewEngine(db, pub, auditor, policy, hmacKey)
	riskEngine.LoadState(ctx)

	riskEngine.UpdateMarketData(&models.MarketData{
		Venue: "mock", MarketID: "MOCK-DEDUP-TEST",
		BidPriceMicros: 300_000, AskPriceMicros: 310_000,
		BidDepth: 5, AskDepth: 5,
		UpdatedAt: time.Now(),
	})

	makeOrder := func() *models.ProposedOrder {
		return &models.ProposedOrder{
			TraceID:        uuid.New().String(),
			StrategyID:     "test-strategy",
			Venue:          "mock",
			MarketID:       "MOCK-DEDUP-TEST",
			Side:           models.SideBuy,
			Quantity:       5,
			PriceMicros:    300_000,
			NotionalMicros: 1_500_000,
			ProposedAt:     time.Now(),
		}
	}

	// First order should be approved
	approval1, err := riskEngine.EvaluateOrder(ctx, makeOrder())
	if err != nil {
		t.Fatalf("first evaluate: %v", err)
	}
	if approval1.Decision != risk.DecisionApproved {
		t.Fatalf("first order should be approved, got %s", approval1.Decision)
	}

	// Second identical order (different trace_id but same idempotency key)
	// should be denied by duplicate_order check
	approval2, err := riskEngine.EvaluateOrder(ctx, makeOrder())
	if err != nil {
		t.Fatalf("second evaluate: %v", err)
	}
	if approval2.Decision != risk.DecisionDenied {
		t.Fatalf("second order should be denied as duplicate, got %s", approval2.Decision)
	}

	found := false
	for _, c := range approval2.Checks {
		if c.Name == "duplicate_order" && !c.Passed {
			found = true
			t.Logf("Correctly caught duplicate: %s", c.Detail)
		}
	}
	if !found {
		t.Error("expected duplicate_order check to fail")
		for _, c := range approval2.Checks {
			if !c.Passed {
				t.Logf("  Failed: %s — %s", c.Name, c.Detail)
			}
		}
	}
}
