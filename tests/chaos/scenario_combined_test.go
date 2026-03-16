//go:build chaos

package chaos

import (
	"context"
	"testing"
	"time"
)

// TestChaos_CombinedFaults verifies system resilience under multiple simultaneous faults:
// stale data + venue errors, then progressive recovery.
func TestChaos_CombinedFaults(t *testing.T) {
	h := NewChaosHarness(t)
	ctx := context.Background()
	marketID := "CHAOS-COMBINED-" + time.Now().Format("150405")
	strategyID := "chaos-combined-test"

	// Step 1: Normal operation baseline
	h.InjectMarketData(marketID)
	order := h.MakeOrder(strategyID, marketID)
	_, err := h.EvalAndSubmit(ctx, order)
	if err != nil {
		t.Fatalf("baseline should succeed: %v", err)
	}
	t.Log("Baseline: OK")

	// Step 2: Inject multiple faults
	InjectStaleMarketData(h.RiskEngine, "mock", marketID, 10*time.Minute)
	h.FaultVenue.InjectError("combined fault: exchange down")

	// Step 3: Attempt orders — should fail at risk check level (stale data)
	order2 := h.MakeOrder(strategyID, marketID)
	approval, _ := h.RiskEngine.EvaluateOrder(ctx, order2)
	if approval != nil && approval.Decision == "approved" {
		t.Error("expected denial with stale data")
	}
	t.Log("Under combined faults: risk correctly denied (stale data)")

	// Step 4: Fix stale data but venue still broken
	InjectFreshMarketData(h.RiskEngine, "mock", marketID)
	order3 := h.MakeOrder(strategyID, marketID)
	_, err = h.EvalAndSubmit(ctx, order3)
	if err == nil {
		t.Error("expected venue error")
	} else {
		t.Logf("Data fresh but venue broken: %v", err)
	}

	// Step 5: Fix venue — full recovery
	h.FaultVenue.Reset()
	h.InjectMarketData(marketID)
	order4 := h.MakeOrder(strategyID, marketID)
	_, err = h.EvalAndSubmit(ctx, order4)
	if err != nil {
		t.Fatalf("full recovery should succeed: %v", err)
	}

	t.Log("Combined faults → progressive recovery: PASSED")
	h.VerifyConsistentState(ctx)
}
