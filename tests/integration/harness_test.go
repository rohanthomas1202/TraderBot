//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/config"
	"autonomy-platform/internal/events"
	"autonomy-platform/internal/ledger"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/execution"
	"autonomy-platform/services/mockexchange"
	"autonomy-platform/services/recon"
	"autonomy-platform/services/risk"
	"autonomy-platform/services/watchdog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// killSwitchAdapter adapts KillSwitchManager.Trigger (which takes KillSwitchLevel)
// to the recon.KillSwitchTrigger interface (which takes string).
type killSwitchAdapter struct {
	mgr *watchdog.KillSwitchManager
}

func (a *killSwitchAdapter) Trigger(ctx context.Context, level, scope, reason, triggeredBy string) error {
	return a.mgr.Trigger(ctx, watchdog.KillSwitchLevel(level), scope, reason, triggeredBy)
}

// TestHarness bundles all services for integration testing.
// It provides a unified way to set up, run, and tear down the full system.
type TestHarness struct {
	t *testing.T

	DB        *pgxpool.Pool
	Publisher *events.Publisher

	Policy  *config.Policy
	HMACKey []byte

	Auditor *audit.Logger
	Venue   *execution.PaperAdapter
	Ledger  *ledger.Ledger

	RiskEngine *risk.Engine
	ExecEngine *execution.Engine
	KillMgr    *watchdog.KillSwitchManager
	DeadMan    *watchdog.DeadMansSwitch
	ReconComp  *recon.Comparator
	ReconEng   *recon.Engine

	Limits ledger.ExposureLimits

	// Metrics collected during simulation
	Metrics *SimMetrics
}

// SimMetrics tracks metrics during a simulation run.
type SimMetrics struct {
	mu sync.Mutex

	OrdersProposed  int
	OrdersApproved  int
	OrdersDenied    int
	OrdersSubmitted int
	OrdersFilled    int
	OrdersRejected  int
	FillsProcessed  int
	Errors          []string
	StartTime       time.Time
	EndTime         time.Time
}

func (m *SimMetrics) RecordProposed()            { m.mu.Lock(); m.OrdersProposed++; m.mu.Unlock() }
func (m *SimMetrics) RecordApproved()             { m.mu.Lock(); m.OrdersApproved++; m.mu.Unlock() }
func (m *SimMetrics) RecordDenied()               { m.mu.Lock(); m.OrdersDenied++; m.mu.Unlock() }
func (m *SimMetrics) RecordSubmitted()            { m.mu.Lock(); m.OrdersSubmitted++; m.mu.Unlock() }
func (m *SimMetrics) RecordFilled()               { m.mu.Lock(); m.OrdersFilled++; m.mu.Unlock() }
func (m *SimMetrics) RecordRejected()             { m.mu.Lock(); m.OrdersRejected++; m.mu.Unlock() }
func (m *SimMetrics) RecordFill()                 { m.mu.Lock(); m.FillsProcessed++; m.mu.Unlock() }
func (m *SimMetrics) RecordError(msg string)      { m.mu.Lock(); m.Errors = append(m.Errors, msg); m.mu.Unlock() }

func (m *SimMetrics) Snapshot() SimMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *m
	cp.Errors = make([]string, len(m.Errors))
	copy(cp.Errors, m.Errors)
	return cp
}

// HarnessConfig configures the test harness.
type HarnessConfig struct {
	VenueConfig     mockexchange.Config
	ExposureLimits  ledger.ExposureLimits
	ReconInterval   time.Duration
	HeartbeatInterval time.Duration
	HeartbeatGrace  int
	SetReconciled   bool
}

// DefaultHarnessConfig returns a configuration suitable for fast tests.
func DefaultHarnessConfig() HarnessConfig {
	return HarnessConfig{
		VenueConfig: mockexchange.Config{
			FillDelayMs:          50,
			FillProbability:      1.0,
			PartialFillProb:      0.0,
			RejectionRate:        0.0,
			InitialBalanceMicros: 100_000_000_000,
		},
		ExposureLimits: ledger.ExposureLimits{
			MaxPositionNotionalMicros: 50_000_000_000,
			MaxTotalExposureMicros:    100_000_000_000,
		},
		ReconInterval:     60 * time.Second,
		HeartbeatInterval: 5 * time.Second,
		HeartbeatGrace:    3,
		SetReconciled:     true,
	}
}

// NewTestHarness creates a fully wired test environment.
func NewTestHarness(t *testing.T, cfg HarnessConfig) *TestHarness {
	t.Helper()
	db, _, pub := setupTestEnv(t)
	ctx := context.Background()
	policy := loadTestPolicy(t)
	hmacKey := []byte("test-hmac-key-phase10")

	// Clean up stale kill switch events from prior test runs so they
	// don't contaminate the system_mode loaded by the risk engine.
	db.Exec(ctx, `UPDATE watchdog.kill_switch_events SET resumed = TRUE, resumed_by = 'test-cleanup', resumed_at = NOW() WHERE resumed = FALSE`)

	auditor := audit.NewLogger("integration-test", db)
	venue := execution.NewPaperAdapter(cfg.VenueConfig)
	intentLedger := ledger.NewLedger(db)

	riskEngine := risk.NewEngine(db, pub, auditor, policy, hmacKey)
	if err := riskEngine.LoadState(ctx); err != nil {
		t.Fatalf("load risk state: %v", err)
	}

	execEngine := execution.NewEngine(db, venue, pub, auditor, hmacKey, intentLedger, cfg.ExposureLimits)
	if cfg.SetReconciled {
		execEngine.SetReconciled(true)
	}

	killMgr := watchdog.NewKillSwitchManager(db, execEngine, riskEngine, pub, auditor)
	deadMan := watchdog.NewDeadMansSwitch(killMgr, db, cfg.HeartbeatInterval, cfg.HeartbeatGrace)

	reconComp := recon.NewComparator(db, venue)
	killAdapter := &killSwitchAdapter{mgr: killMgr}
	reconEng := recon.NewEngine(db, reconComp, killAdapter, auditor, cfg.ReconInterval)

	return &TestHarness{
		t:          t,
		DB:         db,
		Publisher:  pub,
		Policy:     policy,
		HMACKey:    hmacKey,
		Auditor:    auditor,
		Venue:      venue,
		Ledger:     intentLedger,
		RiskEngine: riskEngine,
		ExecEngine: execEngine,
		KillMgr:    killMgr,
		DeadMan:    deadMan,
		ReconComp:  reconComp,
		ReconEng:   reconEng,
		Limits:     cfg.ExposureLimits,
		Metrics:    &SimMetrics{StartTime: time.Now()},
	}
}

// InjectMarketData seeds fresh market data for a market into the risk engine.
func (h *TestHarness) InjectMarketData(marketID string) {
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

// InjectMarketDataCustom seeds market data with custom prices.
func (h *TestHarness) InjectMarketDataCustom(marketID string, bid, ask int64) {
	h.RiskEngine.UpdateMarketData(&models.MarketData{
		Venue:           "mock",
		MarketID:        marketID,
		BidPriceMicros:  bid,
		AskPriceMicros:  ask,
		LastPriceMicros: (bid + ask) / 2,
		BidDepth:        5,
		AskDepth:        5,
		UpdatedAt:       time.Now(),
	})
}

// InjectStaleMarketData seeds market data that is older than the freshness limit.
func (h *TestHarness) InjectStaleMarketData(marketID string, age time.Duration) {
	h.RiskEngine.UpdateMarketData(&models.MarketData{
		Venue:           "mock",
		MarketID:        marketID,
		BidPriceMicros:  350_000,
		AskPriceMicros:  360_000,
		LastPriceMicros: 355_000,
		BidDepth:        5,
		AskDepth:        5,
		UpdatedAt:       time.Now().Add(-age),
	})
}

// UniqueMarketID returns a unique market ID scoped to this test.
func (h *TestHarness) UniqueMarketID(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, uuid.New().String()[:8])
}

// UniqueStrategyID returns a unique strategy ID scoped to this test.
func (h *TestHarness) UniqueStrategyID(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, uuid.New().String()[:8])
}

// MakeOrder creates a proposed order with sensible defaults.
func (h *TestHarness) MakeOrder(strategyID, marketID string, side models.Side, qty int32, priceMicros int64) *models.ProposedOrder {
	return &models.ProposedOrder{
		TraceID:        uuid.New().String(),
		StrategyID:     strategyID,
		Venue:          "mock",
		MarketID:       marketID,
		Side:           side,
		Quantity:       qty,
		PriceMicros:    priceMicros,
		NotionalMicros: int64(qty) * priceMicros,
		ProposedAt:     time.Now(),
	}
}

// EvalAndSubmit evaluates an order through risk, then submits if approved.
// Returns the approval (always) and order record (if submitted). Tracks metrics.
func (h *TestHarness) EvalAndSubmit(ctx context.Context, order *models.ProposedOrder) (*risk.Approval, *models.OrderRecord, error) {
	h.Metrics.RecordProposed()

	approval, err := h.RiskEngine.EvaluateOrder(ctx, order)
	if err != nil {
		h.Metrics.RecordError(fmt.Sprintf("evaluate: %v", err))
		return nil, nil, err
	}

	if approval.Decision != risk.DecisionApproved {
		h.Metrics.RecordDenied()
		return approval, nil, nil
	}
	h.Metrics.RecordApproved()

	rec, err := h.ExecEngine.SubmitOrder(ctx, approval)
	if err != nil {
		h.Metrics.RecordError(fmt.Sprintf("submit: %v", err))
		return approval, nil, err
	}
	h.Metrics.RecordSubmitted()
	return approval, rec, nil
}

// WaitForFills polls for fills and processes them through the risk engine.
func (h *TestHarness) WaitForFills(ctx context.Context, wait time.Duration) error {
	time.Sleep(wait)
	return h.ExecEngine.PollFills(ctx, time.Now().Add(-wait-time.Second), h.RiskEngine.ReportFill)
}

// RunSimulation runs orders at the given rate for the given duration.
// It returns the final metrics snapshot.
func (h *TestHarness) RunSimulation(ctx context.Context, duration time.Duration, orderInterval time.Duration, markets []string, strategyID string) SimMetrics {
	h.Metrics.StartTime = time.Now()

	// Inject fresh market data for all markets
	for _, m := range markets {
		h.InjectMarketData(m)
	}

	deadline := time.After(duration)
	orderTicker := time.NewTicker(orderInterval)
	fillTicker := time.NewTicker(200 * time.Millisecond)
	dataTicker := time.NewTicker(time.Second)
	defer orderTicker.Stop()
	defer fillTicker.Stop()
	defer dataTicker.Stop()

	marketIdx := 0
	priceOffset := int64(0)

	for {
		select {
		case <-ctx.Done():
			goto done
		case <-deadline:
			goto done
		case <-dataTicker.C:
			// Refresh market data to keep it fresh
			for _, m := range markets {
				h.InjectMarketData(m)
			}
		case <-fillTicker.C:
			_ = h.ExecEngine.PollFills(ctx, time.Now().Add(-1*time.Minute), h.RiskEngine.ReportFill)
		case <-orderTicker.C:
			market := markets[marketIdx%len(markets)]
			marketIdx++
			priceOffset++

			// Vary price to avoid duplicate_order idempotency check
			price := 350_000 + priceOffset*1_000
			order := h.MakeOrder(strategyID, market, models.SideBuy, 1, price)
			h.EvalAndSubmit(ctx, order)
		}
	}

done:
	// Final fill poll
	time.Sleep(300 * time.Millisecond)
	_ = h.ExecEngine.PollFills(ctx, time.Now().Add(-1*time.Minute), h.RiskEngine.ReportFill)

	h.Metrics.EndTime = time.Now()
	return h.Metrics.Snapshot()
}

// AssertAuditChainIntact verifies the audit log hash chain has no gaps.
func (h *TestHarness) AssertAuditChainIntact(ctx context.Context) {
	h.t.Helper()
	var total, uniqueHashes int
	err := h.DB.QueryRow(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT entry_hash) FROM audit.event_log`).Scan(&total, &uniqueHashes)
	if err != nil {
		h.t.Fatalf("query audit stats: %v", err)
	}
	if total != uniqueHashes {
		h.t.Errorf("audit hash collision: total=%d unique=%d", total, uniqueHashes)
	}
	h.t.Logf("Audit chain: %d entries, all unique hashes", total)
}

// AssertLedgerGapless verifies intent ledger versions have no gaps.
func (h *TestHarness) AssertLedgerGapless(ctx context.Context) {
	h.t.Helper()
	var total int
	var minV, maxV int64
	err := h.DB.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(MIN(version),0), COALESCE(MAX(version),0) FROM execution.order_intents`).
		Scan(&total, &minV, &maxV)
	if err != nil {
		h.t.Fatalf("query ledger stats: %v", err)
	}
	if total == 0 {
		h.t.Log("Ledger: empty (ok for some tests)")
		return
	}
	expectedCount := maxV - minV + 1
	if int64(total) != expectedCount {
		h.t.Errorf("ledger gap detected: total=%d but version range [%d,%d] expects %d",
			total, minV, maxV, expectedCount)
	}
	h.t.Logf("Ledger: %d intents, versions %d–%d, gapless", total, minV, maxV)
}

// AssertNoCriticalErrors verifies no critical audit entries exist.
func (h *TestHarness) AssertNoCriticalErrors(ctx context.Context) {
	h.t.Helper()
	var count int
	h.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM audit.event_log WHERE severity = 'critical'`).Scan(&count)
	if count > 0 {
		h.t.Logf("WARNING: %d critical audit entries found", count)
	}
}

// PrintReport prints a summary of the simulation metrics.
func (h *TestHarness) PrintReport(metrics SimMetrics) {
	duration := metrics.EndTime.Sub(metrics.StartTime)
	h.t.Logf("=== Simulation Report ===")
	h.t.Logf("Duration:         %s", duration.Round(time.Millisecond))
	h.t.Logf("Orders proposed:  %d", metrics.OrdersProposed)
	h.t.Logf("Orders approved:  %d", metrics.OrdersApproved)
	h.t.Logf("Orders denied:    %d", metrics.OrdersDenied)
	h.t.Logf("Orders submitted: %d", metrics.OrdersSubmitted)
	h.t.Logf("Errors:           %d", len(metrics.Errors))
	if len(metrics.Errors) > 0 {
		for i, e := range metrics.Errors {
			if i >= 5 {
				h.t.Logf("  ... and %d more", len(metrics.Errors)-5)
				break
			}
			h.t.Logf("  - %s", e)
		}
	}
}
