//go:build chaos

package chaos

import (
	"context"
	"testing"
	"time"

	"autonomy-platform/services/risk"
)

// TestChaos_StaleDataRejection verifies that stale market data causes order rejection,
// and that refreshed data allows orders through again.
func TestChaos_StaleDataRejection(t *testing.T) {
	h := NewChaosHarness(t)
	ctx := context.Background()
	marketID := "CHAOS-STALE-" + time.Now().Format("150405")
	strategyID := "chaos-stale-test"

	// Step 1: Inject stale data (10 minutes old, policy limit is typically 30s)
	InjectStaleMarketData(h.RiskEngine, "mock", marketID, 10*time.Minute)

	// Step 2: Try to submit an order — should be denied by data_freshness check
	order := h.MakeOrder(strategyID, marketID)
	approval, err := h.RiskEngine.EvaluateOrder(ctx, order)
	if err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	if approval.Decision != risk.DecisionDenied {
		t.Fatalf("expected denial for stale data, got %s", approval.Decision)
	}

	// Verify it was the data_freshness check that failed
	freshnessFailed := false
	for _, c := range approval.Checks {
		if c.Name == "data_freshness" && !c.Passed {
			freshnessFailed = true
			t.Logf("data_freshness correctly failed: %s", c.Detail)
		}
	}
	if !freshnessFailed {
		t.Error("expected data_freshness check to fail")
	}

	// Step 3: Inject fresh data
	InjectFreshMarketData(h.RiskEngine, "mock", marketID)

	// Step 4: Try again — should be approved
	order2 := h.MakeOrder(strategyID, marketID)
	approval2, err := h.RiskEngine.EvaluateOrder(ctx, order2)
	if err != nil {
		t.Fatalf("evaluate failed after refresh: %v", err)
	}
	if approval2.Decision != risk.DecisionApproved {
		var failedChecks []string
		for _, c := range approval2.Checks {
			if !c.Passed {
				failedChecks = append(failedChecks, c.Name+": "+c.Detail)
			}
		}
		t.Fatalf("expected approval after data refresh, got %s (failed: %v)", approval2.Decision, failedChecks)
	}

	t.Log("Stale data → rejection → fresh data → approval: PASSED")
	h.VerifyConsistentState(ctx)
}
