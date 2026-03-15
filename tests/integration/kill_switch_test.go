//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/execution"
	"autonomy-platform/services/risk"
	"autonomy-platform/services/watchdog"

	"github.com/google/uuid"
)

// TestKillSwitch_BlocksNewOrders verifies that after a kill switch is triggered,
// the execution engine rejects new order submissions.
func TestKillSwitch_BlocksNewOrders(t *testing.T) {
	db, _, pub := setupTestEnv(t)
	ctx := context.Background()
	policy := loadTestPolicy(t)
	hmacKey := []byte("test-hmac-key")

	auditor := audit.NewLogger("test", db)

	// Create execution engine
	venue := execution.NewMockAdapter("localhost:50060")
	execEngine := execution.NewEngine(db, venue, pub, auditor, hmacKey)

	// Create kill switch manager connected to execution engine
	killMgr := watchdog.NewKillSwitchManager(db, execEngine, nil, pub, auditor)

	// Trigger kill switch
	err := killMgr.Trigger(ctx, watchdog.LevelCancelOnly, "global", "test kill switch", "test")
	if err != nil {
		t.Fatalf("trigger kill switch: %v", err)
	}

	// Verify system mode changed
	if killMgr.GetCurrentMode() != "cancel_only" {
		t.Fatalf("expected cancel_only mode, got %s", killMgr.GetCurrentMode())
	}

	// Create a valid order and approval
	riskEngine := risk.NewEngine(db, pub, auditor, policy, hmacKey)
	riskEngine.LoadState(ctx)
	riskEngine.UpdateMarketData(&models.MarketData{
		Venue: "mock", MarketID: "MOCK-KILL-TEST",
		BidPriceMicros: 400_000, AskPriceMicros: 410_000,
		UpdatedAt: time.Now(),
	})

	// The risk engine should deny because system mode is not normal.
	// But even if we forge an approval, the execution engine should reject.
	order := &models.ProposedOrder{
		TraceID: uuid.New().String(), StrategyID: "test",
		Venue: "mock", MarketID: "MOCK-KILL-TEST",
		Side: models.SideBuy, Quantity: 1,
		PriceMicros: 400_000, NotionalMicros: 400_000,
		ProposedAt: time.Now(),
	}

	approval, _ := riskEngine.EvaluateOrder(ctx, order)

	// The risk engine should have denied this (system_mode check)
	if approval.Decision == risk.DecisionApproved {
		t.Log("WARNING: risk engine approved during kill switch — this means system_mode wasn't propagated to risk engine state yet")
	}

	// Try submitting to execution engine (should fail regardless)
	_, err = execEngine.SubmitOrder(ctx, approval)
	if err == nil {
		t.Fatal("expected execution engine to reject order during kill switch")
	}
	t.Logf("Correctly rejected: %v", err)

	// Verify kill switch event is in DB
	var count int
	db.QueryRow(ctx,
		`SELECT COUNT(*) FROM watchdog.kill_switch_events WHERE scope = 'global' AND resumed = FALSE`).Scan(&count)
	if count == 0 {
		t.Error("expected kill switch event in database")
	}
}

// TestKillSwitch_AckResumeFlow verifies the full ack → resume lifecycle.
func TestKillSwitch_AckResumeFlow(t *testing.T) {
	db, _, pub := setupTestEnv(t)
	ctx := context.Background()
	auditor := audit.NewLogger("test", db)

	venue := execution.NewMockAdapter("localhost:50060")
	execEngine := execution.NewEngine(db, venue, pub, auditor, []byte("test"))
	killMgr := watchdog.NewKillSwitchManager(db, execEngine, nil, pub, auditor)

	// Trigger
	killMgr.Trigger(ctx, watchdog.LevelSoftPause, "strategy:test-strat", "test", "test-operator")

	if killMgr.GetCurrentMode() != "soft_pause" {
		t.Fatalf("expected soft_pause, got %s", killMgr.GetCurrentMode())
	}

	// Try to resume without ack — should fail
	err := killMgr.Resume(ctx, "strategy:test-strat", "test-operator")
	if err == nil {
		t.Fatal("expected error: must acknowledge before resuming")
	}
	t.Logf("Correctly blocked resume without ack: %v", err)

	// Acknowledge
	err = killMgr.Acknowledge(ctx, "strategy:test-strat", "test-operator", "investigated, was a test")
	if err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	// Resume
	err = killMgr.Resume(ctx, "strategy:test-strat", "test-operator")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	if killMgr.GetCurrentMode() != "normal" {
		t.Fatalf("expected normal after resume, got %s", killMgr.GetCurrentMode())
	}
}

// TestKillSwitch_PersistsSurvivesRestart verifies halts survive process restart.
func TestKillSwitch_PersistsSurvivesRestart(t *testing.T) {
	db, _, pub := setupTestEnv(t)
	ctx := context.Background()
	auditor := audit.NewLogger("test", db)

	killMgr1 := watchdog.NewKillSwitchManager(db, nil, nil, pub, auditor)

	// Trigger a halt
	killMgr1.Trigger(ctx, watchdog.LevelCancelOnly, "global", "persistence test", "test")

	// Create a NEW kill switch manager (simulating restart)
	killMgr2 := watchdog.NewKillSwitchManager(db, nil, nil, pub, auditor)
	killMgr2.LoadActiveHalts(ctx)

	// Should still be in cancel_only mode
	if killMgr2.GetCurrentMode() != "cancel_only" {
		t.Fatalf("expected cancel_only after restart, got %s", killMgr2.GetCurrentMode())
	}

	halts := killMgr2.GetActiveHalts()
	if len(halts) == 0 {
		t.Fatal("expected active halts after restart")
	}
	t.Logf("Halt survived restart: level=%s scope=%s reason=%s", halts[0].Level, halts[0].Scope, halts[0].Reason)

	// Cleanup: ack and resume
	killMgr2.Acknowledge(ctx, "global", "test", "test cleanup")
	killMgr2.Resume(ctx, "global", "test")
}
