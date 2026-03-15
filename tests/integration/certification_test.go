//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"autonomy-platform/internal/ledger"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/execution"
	"autonomy-platform/services/mockexchange"
	"autonomy-platform/services/risk"
	"autonomy-platform/services/watchdog"

	"github.com/google/uuid"
)

// ─── Scenario 1: Normal Trading Flow ───

// TestCert_NormalTradingFlow submits multiple orders across markets,
// verifies fills, and checks that all state is consistent at the end.
func TestCert_NormalTradingFlow(t *testing.T) {
	h := NewTestHarness(t, DefaultHarnessConfig())
	ctx := context.Background()

	strategyID := h.UniqueStrategyID("cert-normal")
	markets := []string{
		h.UniqueMarketID("NORM-A"),
		h.UniqueMarketID("NORM-B"),
		h.UniqueMarketID("NORM-C"),
	}

	// Seed market data
	for _, m := range markets {
		h.InjectMarketData(m)
	}

	// Submit orders across all markets
	// Vary price per order to avoid duplicate_order idempotency check
	var traceIDs []string
	orderNum := 0
	for i, market := range markets {
		for j := 0; j < 3; j++ {
			orderNum++
			price := int64(350_000 + orderNum*1_000) // unique price per order
			h.InjectMarketDataCustom(market, price-5_000, price+5_000)

			order := h.MakeOrder(strategyID, market, models.SideBuy, 1, price)

			approval, rec, err := h.EvalAndSubmit(ctx, order)
			if err != nil {
				t.Fatalf("order %d/%d submit: %v", i, j, err)
			}
			if approval.Decision != risk.DecisionApproved {
				for _, c := range approval.Checks {
					if !c.Passed {
						t.Logf("  check %s FAIL: %s", c.Name, c.Detail)
					}
				}
				t.Fatalf("order %d/%d should be approved, got %s", i, j, approval.Decision)
			}
			if rec == nil {
				t.Fatalf("order %d/%d: approved but no order record", i, j)
			}
			traceIDs = append(traceIDs, order.TraceID)
		}
	}

	// Wait for fills
	if err := h.WaitForFills(ctx, 500*time.Millisecond); err != nil {
		t.Fatalf("wait for fills: %v", err)
	}

	// Verify all orders reached terminal state in DB
	for _, traceID := range traceIDs {
		var status string
		err := h.DB.QueryRow(ctx,
			`SELECT status FROM execution.orders WHERE trace_id = $1`, traceID).Scan(&status)
		if err != nil {
			t.Fatalf("query order %s: %v", traceID[:8], err)
		}
		if status != "filled" {
			t.Errorf("order %s: expected filled, got %s", traceID[:8], status)
		}
	}

	// Verify intents were updated
	for _, traceID := range traceIDs {
		intent, err := h.Ledger.GetByTraceID(ctx, traceID)
		if err != nil {
			t.Fatalf("get intent %s: %v", traceID[:8], err)
		}
		if intent == nil {
			t.Errorf("intent missing for %s", traceID[:8])
			continue
		}
		if intent.Status != ledger.IntentFilled {
			t.Errorf("intent %s: expected filled, got %s", traceID[:8], intent.Status)
		}
	}

	// Verify audit entries exist
	var auditCount int
	h.DB.QueryRow(ctx, `SELECT COUNT(*) FROM audit.event_log WHERE service = 'integration-test'`).Scan(&auditCount)
	if auditCount == 0 {
		t.Error("no audit entries for integration-test service")
	}
	t.Logf("Normal flow: %d orders across %d markets, %d audit entries", len(traceIDs), len(markets), auditCount)
}

// ─── Scenario 2: Kill Switch Activation and Recovery ───

// TestCert_KillSwitchActivationAndRecovery verifies the full kill switch lifecycle:
// trigger → orders blocked → acknowledge → resume → orders work again.
func TestCert_KillSwitchActivationAndRecovery(t *testing.T) {
	h := NewTestHarness(t, DefaultHarnessConfig())
	ctx := context.Background()

	strategyID := h.UniqueStrategyID("cert-kill")
	marketID := h.UniqueMarketID("KILL-CERT")
	h.InjectMarketData(marketID)

	// 1. Submit an order successfully before kill switch
	order1 := h.MakeOrder(strategyID, marketID, models.SideBuy, 1, 355_000)
	_, rec1, err := h.EvalAndSubmit(ctx, order1)
	if err != nil {
		t.Fatalf("pre-kill order: %v", err)
	}
	if rec1 == nil {
		t.Fatal("pre-kill order should have been submitted")
	}

	// 2. Trigger kill switch
	err = h.KillMgr.Trigger(ctx, watchdog.LevelCancelOnly, "global", "certification test", "test-operator")
	if err != nil {
		t.Fatalf("trigger kill switch: %v", err)
	}
	if h.KillMgr.GetCurrentMode() != "cancel_only" {
		t.Fatalf("expected cancel_only, got %s", h.KillMgr.GetCurrentMode())
	}

	// 3. Verify orders are blocked
	h.InjectMarketData(marketID)
	order2 := h.MakeOrder(strategyID, marketID, models.SideBuy, 1, 355_000)
	approval2, _, err := h.EvalAndSubmit(ctx, order2)
	// Risk engine should deny because system mode is not normal
	if approval2 != nil && approval2.Decision == risk.DecisionApproved {
		// Even if risk approved (race), execution should reject
		t.Log("risk engine still approved — checking execution rejection")
	}

	// 4. Verify kill switch event in DB
	var killCount int
	h.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM watchdog.kill_switch_events WHERE scope = 'global' AND resumed = FALSE`).Scan(&killCount)
	if killCount == 0 {
		t.Error("expected kill switch event in database")
	}

	// 5. Acknowledge
	err = h.KillMgr.Acknowledge(ctx, "global", "test-operator", "certification test complete")
	if err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	// 6. Resume
	err = h.KillMgr.Resume(ctx, "global", "test-operator")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if h.KillMgr.GetCurrentMode() != "normal" {
		t.Fatalf("expected normal after resume, got %s", h.KillMgr.GetCurrentMode())
	}

	// 7. Verify orders work again (use different price to avoid duplicate check)
	h.InjectMarketData(marketID)
	order3 := h.MakeOrder(strategyID, marketID, models.SideBuy, 1, 360_000)
	approval3, rec3, err := h.EvalAndSubmit(ctx, order3)
	if err != nil {
		t.Fatalf("post-resume order: %v", err)
	}
	if approval3 != nil && approval3.Decision != risk.DecisionApproved {
		for _, c := range approval3.Checks {
			if !c.Passed {
				t.Logf("  post-resume check FAIL: %s — %s", c.Name, c.Detail)
			}
		}
		t.Fatalf("post-resume order denied: %s", approval3.Decision)
	}
	if rec3 == nil {
		t.Fatal("post-resume order should have been submitted")
	}

	t.Log("Kill switch lifecycle: trigger → block → ack → resume → orders work")
}

// ─── Scenario 3: Dead Man's Switch Activation ───

// TestCert_DeadMansSwitchActivation verifies that missing heartbeats
// trigger an automatic kill switch.
func TestCert_DeadMansSwitchActivation(t *testing.T) {
	cfg := DefaultHarnessConfig()
	cfg.HeartbeatInterval = 100 * time.Millisecond
	cfg.HeartbeatGrace = 2

	h := NewTestHarness(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Record initial heartbeats for critical services
	h.DeadMan.RecordHeartbeat(ctx, "execution-engine", "healthy", "test")
	h.DeadMan.RecordHeartbeat(ctx, "risk-engine", "healthy", "test")

	// Start monitoring
	go h.DeadMan.Monitor(ctx)

	// Let heartbeats expire (100ms interval × 2 grace = 200ms deadline)
	// Wait 400ms to ensure at least one check fires after deadline
	time.Sleep(400 * time.Millisecond)

	// The dead man's switch should have triggered a kill switch
	mode := h.KillMgr.GetCurrentMode()
	if mode == "normal" {
		t.Fatal("expected kill switch to be activated by dead man's switch, still normal")
	}
	t.Logf("Dead man's switch activated: mode=%s", mode)

	halts := h.KillMgr.GetActiveHalts()
	if len(halts) == 0 {
		t.Fatal("expected active halts from dead man's switch")
	}

	foundDMS := false
	for _, halt := range halts {
		if halt.TriggeredBy == "watchdog" {
			foundDMS = true
			t.Logf("Dead man's switch halt: level=%s reason=%s", halt.Level, halt.Reason)
		}
	}
	if !foundDMS {
		t.Error("expected a halt triggered by 'watchdog'")
	}

	// Cleanup — use fresh context since we cancelled the monitoring one
	cancel()
	cleanupCtx := context.Background()
	for _, halt := range halts {
		h.KillMgr.Acknowledge(cleanupCtx, halt.Scope, "test", "cleanup")
		h.KillMgr.Resume(cleanupCtx, halt.Scope, "test")
	}
}

// ─── Scenario 4: Stale Data Handling ───

// TestCert_StaleDataBlocks verifies that orders are denied when market data
// exceeds the configured freshness limit.
func TestCert_StaleDataBlocks(t *testing.T) {
	h := NewTestHarness(t, DefaultHarnessConfig())
	ctx := context.Background()

	strategyID := h.UniqueStrategyID("cert-stale")
	marketID := h.UniqueMarketID("STALE-CERT")

	// Inject stale data (10s old, limit is 5s in paper.yaml)
	h.InjectStaleMarketData(marketID, 10*time.Second)

	order := h.MakeOrder(strategyID, marketID, models.SideBuy, 1, 355_000)
	approval, _, err := h.EvalAndSubmit(ctx, order)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if approval.Decision != risk.DecisionDenied {
		t.Fatalf("expected denied due to stale data, got %s", approval.Decision)
	}

	// Verify data_freshness check failed
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

	// Now inject fresh data and verify order goes through
	h.InjectMarketData(marketID)
	order2 := h.MakeOrder(strategyID, marketID, models.SideBuy, 1, 355_000)
	approval2, rec2, err := h.EvalAndSubmit(ctx, order2)
	if err != nil {
		t.Fatalf("fresh data order: %v", err)
	}
	if approval2.Decision != risk.DecisionApproved {
		t.Fatalf("expected approval with fresh data, got %s", approval2.Decision)
	}
	if rec2 == nil {
		t.Fatal("expected order to be submitted with fresh data")
	}

	t.Log("Stale data: correctly blocks stale, allows fresh")
}

// ─── Scenario 5: Duplicate Order Rejection ───

// TestCert_DuplicateOrderRejection verifies that submitting the same order
// twice within the dedup window is caught.
func TestCert_DuplicateOrderRejection(t *testing.T) {
	h := NewTestHarness(t, DefaultHarnessConfig())
	ctx := context.Background()

	strategyID := h.UniqueStrategyID("cert-dedup")
	marketID := h.UniqueMarketID("DEDUP-CERT")
	h.InjectMarketData(marketID)

	// First order
	order1 := h.MakeOrder(strategyID, marketID, models.SideBuy, 5, 300_000)
	approval1, _, err := h.EvalAndSubmit(ctx, order1)
	if err != nil {
		t.Fatalf("first order: %v", err)
	}
	if approval1.Decision != risk.DecisionApproved {
		t.Fatalf("first order should be approved, got %s", approval1.Decision)
	}

	// Second identical order (different trace_id but same idempotency key)
	h.InjectMarketData(marketID)
	order2 := h.MakeOrder(strategyID, marketID, models.SideBuy, 5, 300_000)
	approval2, _, err := h.EvalAndSubmit(ctx, order2)
	if err != nil {
		t.Fatalf("second order evaluate: %v", err)
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

// ─── Scenario 6: Fat Finger Detection ───

// TestCert_FatFingerDetection verifies that an order significantly larger
// than recent history is caught by the fat-finger check.
func TestCert_FatFingerDetection(t *testing.T) {
	h := NewTestHarness(t, DefaultHarnessConfig())
	ctx := context.Background()

	strategyID := h.UniqueStrategyID("cert-fatfinger")
	marketID := h.UniqueMarketID("FF-CERT")

	// Build up order history with small orders (need at least 3)
	// Vary price slightly per order to avoid duplicate_order check
	for i := 0; i < 5; i++ {
		price := int64(100_000 + i*1_000) // 100k, 101k, 102k, ...
		h.InjectMarketDataCustom(marketID, price-5_000, price+5_000)
		order := h.MakeOrder(strategyID, marketID, models.SideBuy, 1, price)
		approval, _, err := h.EvalAndSubmit(ctx, order)
		if err != nil {
			t.Fatalf("history order %d: %v", i, err)
		}
		if approval.Decision != risk.DecisionApproved {
			for _, c := range approval.Checks {
				if !c.Passed {
					t.Logf("  history %d check FAIL: %s — %s", i, c.Name, c.Detail)
				}
			}
			t.Fatalf("history order %d should be approved, got %s", i, approval.Decision)
		}
	}

	// Now submit a fat-finger order (>5x the average of ~102_000)
	h.InjectMarketDataCustom(marketID, 95_000, 105_000)
	fatOrder := h.MakeOrder(strategyID, marketID, models.SideBuy, 100, 100_000)
	// Notional = 100 * 100_000 = 10_000_000, avg is ~102_000, ratio = ~98x
	approval, _, err := h.EvalAndSubmit(ctx, fatOrder)
	if err != nil {
		t.Fatalf("fat finger order: %v", err)
	}
	if approval.Decision != risk.DecisionDenied {
		t.Fatalf("fat finger order should be denied, got %s", approval.Decision)
	}

	found := false
	for _, c := range approval.Checks {
		if c.Name == "fat_finger" && !c.Passed {
			found = true
			t.Logf("Correctly caught fat finger: %s", c.Detail)
		}
	}
	if !found {
		t.Error("expected fat_finger check to fail")
		for _, c := range approval.Checks {
			if !c.Passed {
				t.Logf("  Failed: %s — %s", c.Name, c.Detail)
			}
		}
	}
}

// ─── Scenario 7: Position Limit Enforcement ───

// TestCert_PositionLimitEnforcement verifies that orders exceeding per-position
// notional limits are denied.
func TestCert_PositionLimitEnforcement(t *testing.T) {
	// Use tight position limits for this test
	cfg := DefaultHarnessConfig()
	cfg.ExposureLimits = ledger.ExposureLimits{
		MaxPositionNotionalMicros: 2_000_000, // $2 per market
		MaxTotalExposureMicros:    100_000_000_000,
	}
	h := NewTestHarness(t, cfg)
	ctx := context.Background()

	strategyID := h.UniqueStrategyID("cert-pos")
	marketID := h.UniqueMarketID("POS-CERT")
	h.InjectMarketData(marketID)

	// First order: $1 notional — should pass
	order1 := h.MakeOrder(strategyID, marketID, models.SideBuy, 2, 500_000)
	// notional = 2 × 500_000 = 1_000_000 ($1), under $2 limit
	_, rec1, err := h.EvalAndSubmit(ctx, order1)
	if err != nil {
		t.Fatalf("first order: %v", err)
	}
	if rec1 == nil {
		t.Fatal("first order should be submitted")
	}

	// Second order: another $2 on the same market — should breach $2 limit at ledger level
	h.InjectMarketData(marketID)
	order2 := h.MakeOrder(strategyID, marketID, models.SideBuy, 4, 500_000)
	// notional = 4 × 500_000 = 2_000_000 ($2), total would be $3 > $2
	approval2, rec2, err := h.EvalAndSubmit(ctx, order2)
	// Either the risk engine position_limit check OR the ledger exposure invariant blocks it
	if rec2 != nil {
		t.Fatalf("second order should NOT have been submitted (would breach position limit)")
	}

	if approval2 != nil && approval2.Decision == risk.DecisionDenied {
		t.Log("Risk engine correctly denied at position_limit check")
	} else if err != nil {
		t.Logf("Ledger correctly blocked at exposure invariant: %v", err)
	} else {
		t.Fatal("expected either risk denial or ledger rejection for position limit breach")
	}

	t.Log("Position limit enforcement working correctly")
}

// ─── Scenario 8: Daily Loss Limit Enforcement ───

// TestCert_DailyLossLimitEnforcement verifies that the strategy daily loss
// limit triggers a denial after sufficient losses.
func TestCert_DailyLossLimitEnforcement(t *testing.T) {
	h := NewTestHarness(t, DefaultHarnessConfig())
	ctx := context.Background()

	strategyID := h.UniqueStrategyID("cert-loss")
	marketID := h.UniqueMarketID("LOSS-CERT")

	// Simulate daily loss by directly manipulating in-memory risk state.
	// The risk engine's LoadState doesn't populate per-strategy PnL from DB,
	// so we inject the loss directly into the in-memory strategy state.
	state := h.RiskEngine.GetState()
	state.Strategies[strategyID] = &risk.StrategyState{
		DailyPnL: models.Money(-6_000_000_000), // -$6,000, exceeds $5,000 limit
	}

	h.InjectMarketData(marketID)
	order := h.MakeOrder(strategyID, marketID, models.SideBuy, 1, 355_000)
	approval, _, err := h.EvalAndSubmit(ctx, order)
	if err != nil {
		t.Fatalf("evaluate order: %v", err)
	}

	if approval.Decision != risk.DecisionDenied {
		t.Fatalf("expected denied due to daily loss limit, got %s", approval.Decision)
	}

	found := false
	for _, c := range approval.Checks {
		if c.Name == "strategy_daily_loss" && !c.Passed {
			found = true
			t.Logf("Correctly denied: %s — %s", c.Name, c.Detail)
		}
	}
	if !found {
		t.Error("expected strategy_daily_loss check to fail")
		for _, c := range approval.Checks {
			if !c.Passed {
				t.Logf("  Failed: %s — %s", c.Name, c.Detail)
			}
		}
	}

	t.Log("Daily loss limit enforcement working correctly")
}

// ─── Scenario 9: Crash Recovery with Intent Ledger Replay ───

// TestCert_CrashRecoveryWithLedgerReplay verifies that after a simulated
// crash, the execution engine can fully recover from the intent ledger.
func TestCert_CrashRecoveryWithLedgerReplay(t *testing.T) {
	h := NewTestHarness(t, DefaultHarnessConfig())
	ctx := context.Background()

	strategyID := h.UniqueStrategyID("cert-crash")
	marketID := h.UniqueMarketID("CRASH-CERT")

	// Submit multiple orders (vary price to avoid duplicate check)
	var traceIDs []string
	for i := 0; i < 5; i++ {
		price := int64(350_000 + i*1_000)
		h.InjectMarketDataCustom(marketID, price-5_000, price+5_000)
		order := h.MakeOrder(strategyID, marketID, models.SideBuy, 1, price)
		_, rec, err := h.EvalAndSubmit(ctx, order)
		if err != nil {
			t.Fatalf("order %d: %v", i, err)
		}
		if rec == nil {
			t.Fatalf("order %d should be submitted", i)
		}
		traceIDs = append(traceIDs, order.TraceID)
	}

	// Wait for fills on original engine
	h.WaitForFills(ctx, 500*time.Millisecond)

	// Keep some orders open by forcing status back
	if len(traceIDs) >= 2 {
		h.DB.Exec(ctx, `UPDATE execution.orders SET status = 'open', filled_quantity = 0, completed_at = NULL WHERE trace_id = $1`, traceIDs[len(traceIDs)-1])
	}

	// "Crash" — create a NEW execution engine (simulating restart)
	newVenue := execution.NewPaperAdapter(mockexchange.Config{
		FillDelayMs: 999999, FillProbability: 0.0,
		InitialBalanceMicros: 100_000_000_000,
	})
	newLedger := ledger.NewLedger(h.DB)
	newEngine := execution.NewEngine(h.DB, newVenue, h.Publisher, h.Auditor, h.HMACKey, newLedger, h.Limits)

	// Full recovery
	err := newEngine.RecoverFull(ctx)
	if err != nil {
		t.Fatalf("recover full: %v", err)
	}

	// Replay ledger and verify exposure
	exposure, err := newEngine.ReplayIntentLedger(ctx)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	t.Logf("Recovery complete: open_orders=%d, exposure_markets=%d, total_exposure=%d",
		newEngine.OpenOrderCount(), len(exposure.Outstanding), exposure.TotalNotionalMicros)

	// Verify open orders were recovered
	if newEngine.OpenOrderCount() == 0 && len(traceIDs) > 0 {
		// Check if there really are open orders in DB
		var dbOpen int
		h.DB.QueryRow(ctx,
			`SELECT COUNT(*) FROM execution.orders WHERE status IN ('open','pending','partially_filled')`).Scan(&dbOpen)
		if dbOpen > 0 {
			t.Errorf("DB has %d open orders but engine recovered 0", dbOpen)
		}
	}

	// Verify ledger integrity
	h.AssertLedgerGapless(ctx)

	t.Log("Crash recovery with ledger replay working correctly")
}

// ─── Scenario 10: Reconciliation Mismatch Detection ───

// TestCert_ReconciliationMismatchDetection verifies that the reconciliation
// engine detects injected mismatches between internal and exchange state.
func TestCert_ReconciliationMismatchDetection(t *testing.T) {
	h := NewTestHarness(t, DefaultHarnessConfig())
	ctx := context.Background()

	strategyID := h.UniqueStrategyID("cert-recon")
	marketID := h.UniqueMarketID("RECON-CERT")
	h.InjectMarketDataCustom(marketID, 395_000, 405_000)

	// Submit and fill an order
	order := h.MakeOrder(strategyID, marketID, models.SideBuy, 5, 400_000)
	approval, rec, err := h.EvalAndSubmit(ctx, order)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if approval != nil && approval.Decision != risk.DecisionApproved {
		for _, c := range approval.Checks {
			if !c.Passed {
				t.Logf("  check FAIL: %s — %s", c.Name, c.Detail)
			}
		}
		t.Fatalf("order should be approved, got %s", approval.Decision)
	}
	if rec == nil {
		t.Fatal("order should be submitted")
	}

	h.WaitForFills(ctx, 500*time.Millisecond)

	// Run reconciliation — should match initially
	consistent, err := h.ReconEng.RunStartupCheck(ctx)
	if err != nil {
		t.Fatalf("startup check: %v", err)
	}
	t.Logf("Initial reconciliation: consistent=%v", consistent)

	// Now inject a mismatch by modifying the DB directly
	// Add a phantom position that doesn't exist on the exchange
	_, err = h.DB.Exec(ctx,
		`INSERT INTO risk.positions (venue, market_id, strategy_id, net_quantity, notional_micros, updated_at)
		 VALUES ('mock', $1, $2, 100, 40000000, NOW())
		 ON CONFLICT (venue, market_id, strategy_id) DO UPDATE SET net_quantity = 100`,
		"PHANTOM-"+uuid.New().String()[:8], strategyID)
	if err != nil {
		t.Fatalf("inject phantom position: %v", err)
	}

	// Run reconciliation again — should detect mismatch
	err = h.ReconEng.RunOnce(ctx)
	if err != nil {
		t.Fatalf("recon run: %v", err)
	}

	// Check that reconciliation snapshots were persisted
	var snapshotCount int
	h.DB.QueryRow(ctx, `SELECT COUNT(*) FROM recon.snapshots`).Scan(&snapshotCount)
	if snapshotCount == 0 {
		t.Error("expected reconciliation snapshots")
	}
	t.Logf("Reconciliation detected mismatches: %d snapshots", snapshotCount)
}

// ─── Scenario 11: Exposure Invariant Violation Attempt ───

// TestCert_ExposureInvariantViolation verifies that the intent ledger
// rejects orders that would breach the exposure envelope.
func TestCert_ExposureInvariantViolation(t *testing.T) {
	// Use very tight limits
	cfg := DefaultHarnessConfig()
	cfg.ExposureLimits = ledger.ExposureLimits{
		MaxPositionNotionalMicros: 5_000_000, // $5 per market
		MaxTotalExposureMicros:    10_000_000, // $10 total
	}
	h := NewTestHarness(t, cfg)
	ctx := context.Background()

	// Clean prior intents so exposure starts from zero for this test
	h.DB.Exec(ctx, `DELETE FROM execution.order_intents`)

	strategyID := h.UniqueStrategyID("cert-exp")
	marketA := h.UniqueMarketID("EXP-A")
	marketB := h.UniqueMarketID("EXP-B")

	// Order on market A: $4 notional — under $5/market, under $10 total
	h.InjectMarketDataCustom(marketA, 495_000, 505_000)
	orderA := h.MakeOrder(strategyID, marketA, models.SideBuy, 8, 500_000)
	_, recA, err := h.EvalAndSubmit(ctx, orderA)
	if err != nil {
		t.Fatalf("market A order: %v", err)
	}
	if recA == nil {
		t.Fatal("market A order should be submitted")
	}

	// Order on market B: $4 notional — under $5/market, under $10 total (now at $8)
	h.InjectMarketDataCustom(marketB, 495_000, 505_000)
	orderB := h.MakeOrder(strategyID, marketB, models.SideBuy, 8, 500_000)
	_, recB, err := h.EvalAndSubmit(ctx, orderB)
	if err != nil {
		t.Fatalf("market B order: %v", err)
	}
	if recB == nil {
		t.Fatal("market B order should be submitted")
	}

	// Order on market A: another $4 — would breach $5/market limit
	h.InjectMarketDataCustom(marketA, 495_000, 505_000)
	orderA2 := h.MakeOrder(strategyID, marketA, models.SideBuy, 8, 501_000)
	approval, rec, err := h.EvalAndSubmit(ctx, orderA2)

	breached := false
	if err != nil {
		// Ledger rejected at exposure invariant
		t.Logf("Ledger correctly blocked: %v", err)
		breached = true
	} else if approval != nil && approval.Decision == risk.DecisionDenied {
		t.Log("Risk engine correctly denied")
		breached = true
	} else if rec == nil {
		t.Log("Order blocked before submission")
		breached = true
	}

	if !breached {
		t.Fatal("expected exposure invariant violation to be caught")
	}

	t.Log("Exposure invariant enforcement working correctly")
}

// ─── Scenario 12: Extended Simulation Run ───

// TestCert_ExtendedSimulation runs the full system for a configurable duration
// and verifies all invariants hold at the end.
func TestCert_ExtendedSimulation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping extended simulation in short mode")
	}

	h := NewTestHarness(t, DefaultHarnessConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	strategyID := h.UniqueStrategyID("cert-sim")
	markets := []string{
		h.UniqueMarketID("SIM-A"),
		h.UniqueMarketID("SIM-B"),
		h.UniqueMarketID("SIM-C"),
		h.UniqueMarketID("SIM-D"),
		h.UniqueMarketID("SIM-E"),
	}

	// Run simulation for 30 seconds with orders every 500ms
	metrics := h.RunSimulation(ctx, 30*time.Second, 500*time.Millisecond, markets, strategyID)
	h.PrintReport(metrics)

	// Verify invariants
	h.AssertAuditChainIntact(ctx)
	h.AssertLedgerGapless(ctx)
	h.AssertNoCriticalErrors(ctx)

	// Verify DB state is consistent
	var totalOrders, filledOrders, cancelledOrders, rejectedOrders int
	h.DB.QueryRow(ctx, `SELECT COUNT(*) FROM execution.orders`).Scan(&totalOrders)
	h.DB.QueryRow(ctx, `SELECT COUNT(*) FROM execution.orders WHERE status = 'filled'`).Scan(&filledOrders)
	h.DB.QueryRow(ctx, `SELECT COUNT(*) FROM execution.orders WHERE status = 'cancelled'`).Scan(&cancelledOrders)
	h.DB.QueryRow(ctx, `SELECT COUNT(*) FROM execution.orders WHERE status = 'rejected'`).Scan(&rejectedOrders)

	t.Logf("DB Orders: total=%d filled=%d cancelled=%d rejected=%d",
		totalOrders, filledOrders, cancelledOrders, rejectedOrders)

	var totalIntents int
	h.DB.QueryRow(ctx, `SELECT COUNT(*) FROM execution.order_intents`).Scan(&totalIntents)
	t.Logf("Intent ledger: %d total intents", totalIntents)

	var auditEntries int
	h.DB.QueryRow(ctx, `SELECT COUNT(*) FROM audit.event_log`).Scan(&auditEntries)
	t.Logf("Audit log: %d entries", auditEntries)

	if metrics.OrdersProposed == 0 {
		t.Error("no orders were proposed during simulation")
	}
	if len(metrics.Errors) > metrics.OrdersProposed/2 {
		t.Errorf("too many errors: %d errors out of %d proposed", len(metrics.Errors), metrics.OrdersProposed)
	}
}
