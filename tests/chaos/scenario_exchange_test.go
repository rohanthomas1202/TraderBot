//go:build chaos

package chaos

import (
	"context"
	"testing"
	"time"
)

// TestChaos_VenueTimeout verifies that venue timeouts cause orders to fail gracefully
// without corrupting system state.
func TestChaos_VenueTimeout(t *testing.T) {
	h := NewChaosHarness(t)
	ctx := context.Background()
	marketID := "CHAOS-TIMEOUT-" + time.Now().Format("150405")
	strategyID := "chaos-timeout-test"

	h.InjectMarketData(marketID)

	// Step 1: Inject venue timeout
	h.FaultVenue.InjectTimeout(5 * time.Second)

	// Step 2: Try to submit with a short context deadline
	submitCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	order := h.MakeOrder(strategyID, marketID)
	_, err := h.EvalAndSubmit(submitCtx, order)
	if err == nil {
		t.Fatal("expected error from venue timeout")
	}
	t.Logf("Order correctly failed with timeout: %v", err)

	// Step 3: Reset venue and verify system still works
	h.FaultVenue.Reset()
	h.InjectMarketData(marketID) // refresh data

	order2 := h.MakeOrder(strategyID, marketID)
	_, err = h.EvalAndSubmit(ctx, order2)
	if err != nil {
		t.Fatalf("order should succeed after venue recovery: %v", err)
	}

	t.Log("Venue timeout → graceful failure → recovery: PASSED")
	h.VerifyConsistentState(ctx)
}

// TestChaos_VenueError verifies that venue errors are handled cleanly.
func TestChaos_VenueError(t *testing.T) {
	h := NewChaosHarness(t)
	ctx := context.Background()
	marketID := "CHAOS-ERR-" + time.Now().Format("150405")
	strategyID := "chaos-error-test"

	h.InjectMarketData(marketID)

	// Step 1: Inject venue error
	h.FaultVenue.InjectError("exchange service unavailable")

	// Step 2: Submit — should fail cleanly
	order := h.MakeOrder(strategyID, marketID)
	_, err := h.EvalAndSubmit(ctx, order)
	if err == nil {
		t.Fatal("expected error from venue fault")
	}
	t.Logf("Order correctly failed: %v", err)

	// Step 3: Reset and verify recovery
	h.FaultVenue.Reset()
	h.InjectMarketData(marketID)

	order2 := h.MakeOrder(strategyID, marketID)
	_, err = h.EvalAndSubmit(ctx, order2)
	if err != nil {
		t.Fatalf("order should succeed after venue recovery: %v", err)
	}

	t.Log("Venue error → graceful failure → recovery: PASSED")
	h.VerifyConsistentState(ctx)
}
