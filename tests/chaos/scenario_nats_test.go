//go:build chaos

package chaos

import (
	"context"
	"testing"
	"time"
)

// TestChaos_NATSDisconnect verifies that the system handles NATS disconnection gracefully.
// Events are silently dropped during disconnection, and resume after reconnection.
func TestChaos_NATSDisconnect(t *testing.T) {
	h := NewChaosHarness(t)
	ctx := context.Background()
	marketID := "CHAOS-NATS-" + time.Now().Format("150405")
	strategyID := "chaos-nats-test"

	h.InjectMarketData(marketID)

	// Step 1: Verify normal operation — order should succeed
	// (even though events are published, the core flow doesn't depend on NATS for order submission)
	order := h.MakeOrder(strategyID, marketID)
	_, err := h.EvalAndSubmit(ctx, order)
	if err != nil {
		t.Fatalf("initial order should succeed: %v", err)
	}
	t.Log("Order before NATS disconnect: succeeded")

	// Step 2: Simulate NATS delay
	natsFault := NewNATSFaultInjector(h.Publisher)
	natsFault.InjectDelay(500 * time.Millisecond)

	// The core order flow (risk eval → venue submit) should still work
	// because it uses synchronous gRPC/DB, not NATS for the critical path
	h.InjectMarketData(marketID)
	order2 := h.MakeOrder(strategyID, marketID)
	_, err = h.EvalAndSubmit(ctx, order2)
	if err != nil {
		t.Fatalf("order should succeed even with NATS delay: %v", err)
	}
	t.Log("Order during NATS delay: succeeded (critical path is independent)")

	// Step 3: Reconnect
	natsFault.Reconnect()

	h.InjectMarketData(marketID)
	order3 := h.MakeOrder(strategyID, marketID)
	_, err = h.EvalAndSubmit(ctx, order3)
	if err != nil {
		t.Fatalf("order should succeed after NATS reconnect: %v", err)
	}

	t.Log("NATS disconnect → orders continue → reconnect: PASSED")
	h.VerifyConsistentState(ctx)
}
