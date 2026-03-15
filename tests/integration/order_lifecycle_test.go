//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/config"
	"autonomy-platform/internal/events"
	"autonomy-platform/internal/ledger"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/execution"
	"autonomy-platform/services/mockexchange"
	"autonomy-platform/services/risk"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

// setupTestEnv creates real DB and NATS connections for integration tests.
func setupTestEnv(t *testing.T) (*pgxpool.Pool, *nats.Conn, *events.Publisher) {
	t.Helper()
	ctx := context.Background()

	db, err := pgxpool.New(ctx, "postgres://trader:localdev@localhost:5432/autonomy?sslmode=disable")
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}

	nc, err := nats.Connect("nats://localhost:4222")
	if err != nil {
		t.Fatalf("connect to nats: %v", err)
	}

	pub, err := events.NewPublisher(nc)
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}

	t.Cleanup(func() {
		db.Close()
		nc.Close()
	})

	return db, nc, pub
}

func loadTestPolicy(t *testing.T) *config.Policy {
	t.Helper()
	p, err := config.LoadPolicy("../../policies/paper.yaml")
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	return p
}

// TestOrderLifecycle_ProposalToFill verifies the complete happy path:
// strategy proposes → risk approves → execution submits → mock fills → state updated
func TestOrderLifecycle_ProposalToFill(t *testing.T) {
	db, _, pub := setupTestEnv(t)
	ctx := context.Background()
	policy := loadTestPolicy(t)
	hmacKey := []byte("test-hmac-key")

	auditor := audit.NewLogger("test", db)

	// Create risk engine
	riskEngine := risk.NewEngine(db, pub, auditor, policy, hmacKey)
	if err := riskEngine.LoadState(ctx); err != nil {
		t.Fatalf("load risk state: %v", err)
	}

	// Use unique IDs to isolate from prior test runs
	marketID := "MOCK-BTC-" + uuid.New().String()[:8]
	strategyID := "test-strategy-" + uuid.New().String()[:8]

	// Inject mock market data so freshness check passes
	riskEngine.UpdateMarketData(&models.MarketData{
		Venue:           "mock",
		MarketID:        marketID,
		BidPriceMicros:  350_000,
		AskPriceMicros:  360_000,
		LastPriceMicros: 355_000,
		BidDepth:        5,
		AskDepth:        5,
		UpdatedAt:       time.Now(),
	})

	// Create proposed order
	order := &models.ProposedOrder{
		TraceID:        uuid.New().String(),
		StrategyID:     strategyID,
		Venue:          "mock",
		MarketID:       marketID,
		Side:           models.SideBuy,
		Quantity:       1,
		PriceMicros:    355_000,
		NotionalMicros: 355_000,
		ProposedAt:     time.Now(),
	}

	// Risk engine evaluates
	approval, err := riskEngine.EvaluateOrder(ctx, order)
	if err != nil {
		t.Fatalf("evaluate order: %v", err)
	}
	if approval.Decision != risk.DecisionApproved {
		for _, c := range approval.Checks {
			if !c.Passed {
				t.Logf("  FAIL: %s — %s", c.Name, c.Detail)
			}
		}
		t.Fatalf("expected approved, got %s", approval.Decision)
	}

	// Create execution engine with paper adapter and intent ledger
	venue := execution.NewPaperAdapter(mockexchange.Config{
		FillDelayMs:     50,
		FillProbability: 1.0, // always fill
		PartialFillProb: 0.0,
		RejectionRate:   0.0,
		InitialBalanceMicros: 100_000_000_000,
	})
	intentLedger := ledger.NewLedger(db)
	limits := ledger.ExposureLimits{
		MaxPositionNotionalMicros: 10_000_000_000,
		MaxTotalExposureMicros:    50_000_000_000,
	}
	execEngine := execution.NewEngine(db, venue, pub, auditor, hmacKey, intentLedger, limits)
	execEngine.SetReconciled(true)

	// Submit the approved order
	rec, err := execEngine.SubmitOrder(ctx, approval)
	if err != nil {
		t.Fatalf("submit order: %v", err)
	}
	if rec.Status != models.StatusOpen {
		t.Fatalf("expected status open, got %s", rec.Status)
	}
	if rec.ExchangeOrderID == "" {
		t.Fatal("expected exchange order ID")
	}

	// Wait for mock fill
	time.Sleep(500 * time.Millisecond)

	// Poll fills
	err = execEngine.PollFills(ctx, time.Now().Add(-1*time.Minute), riskEngine.ReportFill)
	if err != nil {
		t.Fatalf("poll fills: %v", err)
	}

	// Verify order is in DB with correct state
	var status string
	var filledQty int32
	err = db.QueryRow(ctx,
		`SELECT status, filled_quantity FROM execution.orders WHERE trace_id = $1`,
		order.TraceID).Scan(&status, &filledQty)
	if err != nil {
		t.Fatalf("query order: %v", err)
	}

	t.Logf("Final state: status=%s filled=%d", status, filledQty)
	// Mock adapter fills immediately, so status should be "filled"
	if status != "filled" {
		t.Errorf("expected filled status, got %s", status)
	}

	// Verify audit log entry exists
	var auditCount int
	db.QueryRow(ctx,
		`SELECT COUNT(*) FROM audit.event_log WHERE trace_id = $1`,
		order.TraceID).Scan(&auditCount)
	if auditCount == 0 {
		t.Error("expected audit log entries for this trade")
	}
	t.Logf("Audit entries: %d", auditCount)

	// Verify risk decision persisted
	var decision string
	db.QueryRow(ctx,
		`SELECT decision FROM risk.policy_decisions WHERE trace_id = $1`,
		order.TraceID).Scan(&decision)
	if decision != "approved" {
		t.Errorf("expected 'approved' decision in DB, got %s", decision)
	}

	// Verify intent ledger was updated by execution engine
	intent, err := intentLedger.GetByTraceID(ctx, order.TraceID)
	if err != nil {
		t.Fatalf("get intent: %v", err)
	}
	if intent == nil {
		t.Fatal("expected intent in ledger for this order")
	}
	t.Logf("Intent: version=%d status=%s", intent.Version, intent.Status)
	if intent.Status != ledger.IntentFilled {
		t.Errorf("expected intent status 'filled', got %s", intent.Status)
	}
}
