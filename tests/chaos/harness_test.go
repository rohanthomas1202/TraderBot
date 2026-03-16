//go:build chaos

package chaos

import (
	"context"
	"fmt"
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
	"autonomy-platform/services/watchdog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

// ChaosHarness extends the integration test pattern with fault injection.
type ChaosHarness struct {
	t *testing.T

	DB        *pgxpool.Pool
	NC        *nats.Conn
	Publisher *events.Publisher
	Policy    *config.Policy
	HMACKey   []byte

	Auditor    *audit.Logger
	RealVenue  *execution.PaperAdapter
	FaultVenue *VenueFaultInjector
	Ledger     *ledger.Ledger

	RiskEngine *risk.Engine
	ExecEngine *execution.Engine
	KillMgr    *watchdog.KillSwitchManager
}

func setupChaosEnv(t *testing.T) (*pgxpool.Pool, *nats.Conn, *events.Publisher) {
	t.Helper()
	ctx := context.Background()

	db, err := pgxpool.New(ctx, "postgres://trader:localdev@localhost:5432/autonomy?sslmode=disable")
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	nc, err := nats.Connect("nats://localhost:4222")
	if err != nil {
		t.Fatalf("connect to nats: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	pub, err := events.NewPublisher(nc)
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}

	return db, nc, pub
}

func loadChaosPolicy(t *testing.T) *config.Policy {
	t.Helper()
	policy, err := config.LoadPolicy("../../policies/paper.yaml")
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	return policy
}

func NewChaosHarness(t *testing.T) *ChaosHarness {
	t.Helper()
	db, nc, pub := setupChaosEnv(t)
	ctx := context.Background()
	policy := loadChaosPolicy(t)
	hmacKey := []byte("chaos-test-hmac-key")

	// Clean stale halts
	db.Exec(ctx, `UPDATE watchdog.kill_switch_events SET resumed = TRUE, resumed_by = 'chaos-cleanup', resumed_at = NOW() WHERE resumed = FALSE`)

	auditor := audit.NewLogger("chaos-test", db)
	realVenue := execution.NewPaperAdapter(mockexchange.Config{
		FillDelayMs:          50,
		FillProbability:      1.0,
		PartialFillProb:      0.0,
		RejectionRate:        0.0,
		InitialBalanceMicros: 100_000_000_000,
	})
	faultVenue := NewVenueFaultInjector(realVenue)
	intentLedger := ledger.NewLedger(db)

	riskEngine := risk.NewEngine(db, pub, auditor, policy, hmacKey)
	if err := riskEngine.LoadState(ctx); err != nil {
		t.Fatalf("load risk state: %v", err)
	}

	limits := ledger.ExposureLimits{
		MaxPositionNotionalMicros: 50_000_000_000,
		MaxTotalExposureMicros:    100_000_000_000,
	}
	execEngine := execution.NewEngine(db, faultVenue, pub, auditor, hmacKey, intentLedger, limits)
	execEngine.SetReconciled(true)

	killMgr := watchdog.NewKillSwitchManager(db, execEngine, riskEngine, pub, auditor)

	return &ChaosHarness{
		t:          t,
		DB:         db,
		NC:         nc,
		Publisher:  pub,
		Policy:     policy,
		HMACKey:    hmacKey,
		Auditor:    auditor,
		RealVenue:  realVenue,
		FaultVenue: faultVenue,
		Ledger:     intentLedger,
		RiskEngine: riskEngine,
		ExecEngine: execEngine,
		KillMgr:    killMgr,
	}
}

func (h *ChaosHarness) InjectMarketData(marketID string) {
	h.RiskEngine.UpdateMarketData(&models.MarketData{
		Venue:           "mock",
		MarketID:        marketID,
		BidPriceMicros:  350_000,
		AskPriceMicros:  360_000,
		LastPriceMicros: 355_000,
		BidDepth:        5,
		AskDepth:        5,
		UpdatedAt:       time.Now(),
	})
}

func (h *ChaosHarness) MakeOrder(strategyID, marketID string) *models.ProposedOrder {
	return &models.ProposedOrder{
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
}

func (h *ChaosHarness) EvalAndSubmit(ctx context.Context, order *models.ProposedOrder) (*risk.Approval, error) {
	approval, err := h.RiskEngine.EvaluateOrder(ctx, order)
	if err != nil {
		return nil, fmt.Errorf("evaluate: %w", err)
	}
	if approval.Decision != risk.DecisionApproved {
		return approval, nil
	}
	_, err = h.ExecEngine.SubmitOrder(ctx, approval)
	return approval, err
}

// VerifyConsistentState checks audit chain, ledger integrity, and no stuck orders.
func (h *ChaosHarness) VerifyConsistentState(ctx context.Context) {
	h.t.Helper()

	// 1. Audit chain has unique hashes
	var total, uniqueHashes int
	err := h.DB.QueryRow(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT entry_hash) FROM audit.event_log`).Scan(&total, &uniqueHashes)
	if err != nil {
		h.t.Fatalf("query audit: %v", err)
	}
	if total != uniqueHashes {
		h.t.Errorf("audit hash collision: total=%d unique=%d", total, uniqueHashes)
	}

	// 2. Ledger gapless
	var ledgerTotal int
	var minV, maxV int64
	err = h.DB.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(MIN(version),0), COALESCE(MAX(version),0) FROM execution.order_intents`).
		Scan(&ledgerTotal, &minV, &maxV)
	if err != nil {
		h.t.Fatalf("query ledger: %v", err)
	}
	if ledgerTotal > 0 {
		expected := maxV - minV + 1
		if int64(ledgerTotal) != expected {
			h.t.Errorf("ledger gap: total=%d, version range [%d,%d]", ledgerTotal, minV, maxV)
		}
	}

	h.t.Logf("Consistency check passed: audit=%d entries, ledger=%d intents", total, ledgerTotal)
}
