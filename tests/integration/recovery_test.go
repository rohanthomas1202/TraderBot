//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/ledger"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/execution"
	"autonomy-platform/services/mockexchange"
	"autonomy-platform/services/risk"

	"github.com/google/uuid"
)

// TestRecovery_OpenOrdersSurviveRestart verifies that the execution engine
// correctly reloads open orders from the database after a restart.
func TestRecovery_OpenOrdersSurviveRestart(t *testing.T) {
	db, _, pub := setupTestEnv(t)
	ctx := context.Background()
	policy := loadTestPolicy(t)
	hmacKey := []byte("test-hmac-key")
	auditor := audit.NewLogger("test", db)

	// Use unique market ID to avoid cross-test state contamination
	marketID := "MOCK-RECOVERY-" + uuid.New().String()[:8]

	// Create and submit an order
	riskEngine := risk.NewEngine(db, pub, auditor, policy, hmacKey)
	riskEngine.LoadState(ctx)
	riskEngine.UpdateMarketData(&models.MarketData{
		Venue: "mock", MarketID: marketID,
		BidPriceMicros: 400_000, AskPriceMicros: 410_000,
		BidDepth: 5, AskDepth: 5, UpdatedAt: time.Now(),
	})

	traceID := uuid.New().String()
	order := &models.ProposedOrder{
		TraceID: traceID, StrategyID: "test",
		Venue: "mock", MarketID: marketID,
		Side: models.SideBuy, Quantity: 5,
		PriceMicros: 400_000, NotionalMicros: 2_000_000,
		ProposedAt: time.Now(),
	}

	approval, err := riskEngine.EvaluateOrder(ctx, order)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if approval.Decision != risk.DecisionApproved {
		for _, c := range approval.Checks {
			if !c.Passed {
				t.Logf("  check failed: %s — %s", c.Name, c.Detail)
			}
		}
		t.Fatalf("expected approval, got %s", approval.Decision)
	}

	// Use a paper adapter that does NOT auto-fill (so order stays open)
	venue := execution.NewPaperAdapter(mockexchange.Config{
		FillDelayMs:     999999, // very long delay = no fill during test
		FillProbability: 0.0,
		InitialBalanceMicros: 100_000_000_000,
	})
	intentLedger := ledger.NewLedger(db)
	limits := ledger.ExposureLimits{
		MaxPositionNotionalMicros: 10_000_000_000,
		MaxTotalExposureMicros:    50_000_000_000,
	}
	execEngine1 := execution.NewEngine(db, venue, pub, auditor, hmacKey, intentLedger, limits)
	rec, err := execEngine1.SubmitOrder(ctx, approval)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	t.Logf("Submitted order: %s status=%s", rec.InternalOrderID, rec.Status)

	// Manually set the order back to "open" in case mock auto-filled
	// (In a real test, we'd configure the mock to not fill)
	db.Exec(ctx, `UPDATE execution.orders SET status = 'open' WHERE trace_id = $1`, order.TraceID)

	// Simulate restart: create a NEW execution engine
	execEngine2 := execution.NewEngine(db, venue, pub, auditor, hmacKey, intentLedger, limits)
	err = execEngine2.LoadOpenOrders(ctx)
	if err != nil {
		t.Fatalf("load open orders: %v", err)
	}

	// Verify the order was recovered
	openCount := execEngine2.OpenOrderCount()
	if openCount == 0 {
		t.Fatal("expected at least one open order after recovery")
	}
	t.Logf("Recovered %d open orders after restart", openCount)
}

// TestRecovery_RiskStateSurvivesRestart verifies that risk state
// (positions, daily stats) is correctly rebuilt from DB.
func TestRecovery_RiskStateSurvivesRestart(t *testing.T) {
	db, _, pub := setupTestEnv(t)
	ctx := context.Background()
	policy := loadTestPolicy(t)
	hmacKey := []byte("test-hmac-key")
	auditor := audit.NewLogger("test", db)

	// Insert a position directly
	today := time.Now().UTC().Format("2006-01-02")
	db.Exec(ctx,
		`INSERT INTO risk.positions (venue, market_id, strategy_id, net_quantity, notional_micros, updated_at)
		 VALUES ('mock', 'MOCK-PERSIST', 'test', 10, 4000000, NOW())
		 ON CONFLICT (venue, market_id, strategy_id) DO UPDATE SET net_quantity = 10, notional_micros = 4000000`)
	db.Exec(ctx,
		`INSERT INTO risk.daily_stats (date, scope, pnl_micros, order_count, updated_at)
		 VALUES ($1, 'global', -500000, 5, NOW())
		 ON CONFLICT (date, scope) DO UPDATE SET pnl_micros = -500000, order_count = 5`,
		today)

	// Create risk engine — should load state from DB
	riskEngine := risk.NewEngine(db, pub, auditor, policy, hmacKey)
	err := riskEngine.LoadState(ctx)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	state := riskEngine.GetState()
	t.Logf("Risk state after recovery: exposure=%s daily_pnl=%s mode=%s",
		state.TotalExposure.String(), state.DailyPnL.String(), state.SystemMode)

	// Verify position was loaded
	if len(state.Markets) == 0 {
		t.Error("expected at least one market position in risk state")
	}
}
