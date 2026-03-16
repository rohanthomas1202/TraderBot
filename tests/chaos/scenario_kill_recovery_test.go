//go:build chaos

package chaos

import (
	"context"
	"testing"
	"time"

	"autonomy-platform/services/risk"
	"autonomy-platform/services/watchdog"
)

// TestChaos_KillSwitchAndRecovery verifies the full kill switch lifecycle:
// trigger → orders denied → acknowledge → resume → orders accepted again.
func TestChaos_KillSwitchAndRecovery(t *testing.T) {
	h := NewChaosHarness(t)
	ctx := context.Background()
	marketID := "CHAOS-KILL-" + time.Now().Format("150405")
	strategyID := "chaos-kill-test"

	h.InjectMarketData(marketID)

	// Step 1: Verify normal operation
	order := h.MakeOrder(strategyID, marketID)
	approval, err := h.RiskEngine.EvaluateOrder(ctx, order)
	if err != nil {
		t.Fatalf("initial eval: %v", err)
	}
	if approval.Decision != risk.DecisionApproved {
		t.Fatal("initial order should be approved")
	}
	t.Log("Pre-kill: orders approved")

	// Step 2: Trigger kill switch
	err = h.KillMgr.Trigger(ctx, watchdog.LevelCancelOnly, "global", "chaos test kill", "chaos-test")
	if err != nil {
		t.Fatalf("trigger kill: %v", err)
	}
	t.Log("Kill switch triggered")

	// Step 3: Verify orders are denied
	time.Sleep(100 * time.Millisecond) // let state propagate
	h.InjectMarketData(marketID)
	order2 := h.MakeOrder(strategyID, marketID)
	approval2, err := h.RiskEngine.EvaluateOrder(ctx, order2)
	if err != nil {
		t.Fatalf("eval during halt: %v", err)
	}
	if approval2.Decision != risk.DecisionDenied {
		t.Fatal("orders should be denied during kill switch")
	}

	// Verify system_mode check failed
	modeFailed := false
	for _, c := range approval2.Checks {
		if c.Name == "system_mode" && !c.Passed {
			modeFailed = true
			t.Logf("system_mode correctly failed: %s", c.Detail)
		}
	}
	if !modeFailed {
		t.Error("expected system_mode check to fail during halt")
	}

	// Step 4: Acknowledge
	err = h.KillMgr.Acknowledge(ctx, "global", "chaos-test", "chaos testing complete")
	if err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	// Step 5: Resume
	err = h.KillMgr.Resume(ctx, "global", "chaos-test")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	// Step 6: Verify orders work again
	time.Sleep(100 * time.Millisecond)
	h.InjectMarketData(marketID)
	order3 := h.MakeOrder(strategyID, marketID)
	approval3, err := h.RiskEngine.EvaluateOrder(ctx, order3)
	if err != nil {
		t.Fatalf("eval after resume: %v", err)
	}
	if approval3.Decision != risk.DecisionApproved {
		var failed []string
		for _, c := range approval3.Checks {
			if !c.Passed {
				failed = append(failed, c.Name+": "+c.Detail)
			}
		}
		t.Fatalf("orders should be approved after resume, got %s (failed: %v)", approval3.Decision, failed)
	}

	t.Log("Kill → deny → ack → resume → approve: PASSED")
	h.VerifyConsistentState(ctx)
}
