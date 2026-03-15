//go:build integration

package ledger

import (
	"context"
	"sync"
	"testing"

	"autonomy-platform/internal/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	db, err := pgxpool.New(ctx, "postgres://trader:localdev@localhost:5432/autonomy?sslmode=disable")
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Clean up intents from prior runs
	_, err = db.Exec(ctx, "DELETE FROM execution.order_intents")
	if err != nil {
		t.Fatalf("clean intents table: %v", err)
	}
	return db
}

func testIntent(traceID string) *OrderIntent {
	return &OrderIntent{
		TraceID:        traceID,
		ApprovalHMAC:   []byte("test-hmac-signature"),
		StrategyID:     "test-strategy",
		Venue:          "mock",
		MarketID:       "TEST-MARKET-" + uuid.New().String()[:8],
		Side:           models.SideBuy,
		Quantity:       10,
		PriceMicros:    500_000, // $0.50
		NotionalMicros: 5_000_000,
	}
}

func permissiveLimits() ExposureLimits {
	return ExposureLimits{
		MaxPositionNotionalMicros: 100_000_000_000, // $100,000
		MaxTotalExposureMicros:    100_000_000_000,
	}
}

// ─── Monotonic Versioning ───

func TestMonotonicVersioning(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	l := NewLedger(db)
	limits := permissiveLimits()

	const n = 20
	versions := make([]int64, n)

	for i := 0; i < n; i++ {
		intent := testIntent(uuid.New().String())
		result, err := l.Append(ctx, intent, limits)
		if err != nil {
			t.Fatalf("append intent %d: %v", i, err)
		}
		versions[i] = result.Version
	}

	// Verify monotonically increasing and gapless
	for i := 1; i < n; i++ {
		if versions[i] != versions[i-1]+1 {
			t.Errorf("version gap: versions[%d]=%d, versions[%d]=%d",
				i-1, versions[i-1], i, versions[i])
		}
	}
}

func TestMonotonicVersioning_Concurrent(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	l := NewLedger(db)
	limits := permissiveLimits()

	const n = 50
	results := make([]*OrderIntent, n)
	errs := make([]error, n)
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			intent := testIntent(uuid.New().String())
			results[idx], errs[idx] = l.Append(ctx, intent, limits)
		}(i)
	}
	wg.Wait()

	// Collect successful versions
	versionSet := make(map[int64]bool)
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("append %d failed: %v", i, errs[i])
		}
		if versionSet[results[i].Version] {
			t.Errorf("duplicate version %d", results[i].Version)
		}
		versionSet[results[i].Version] = true
	}

	// Verify all versions are present (gapless)
	var minV, maxV int64
	for v := range versionSet {
		if minV == 0 || v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	if int(maxV-minV+1) != n {
		t.Errorf("versions not gapless: min=%d, max=%d, count=%d", minV, maxV, n)
	}
}

// ─── Exposure Calculation ───

func TestExposure_EmptyLedger(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	l := NewLedger(db)

	exposure, err := l.ComputeExposure(ctx)
	if err != nil {
		t.Fatalf("compute exposure: %v", err)
	}
	if exposure.TotalNotionalMicros != 0 {
		t.Errorf("expected zero exposure, got %d", exposure.TotalNotionalMicros)
	}
	if len(exposure.Outstanding) != 0 {
		t.Errorf("expected empty outstanding, got %d entries", len(exposure.Outstanding))
	}
}

func TestExposure_OneBuyIntent(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	l := NewLedger(db)
	limits := permissiveLimits()

	intent := testIntent(uuid.New().String())
	intent.MarketID = "EXPOSURE-TEST"
	result, err := l.Append(ctx, intent, limits)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	exposure, err := l.ComputeExposure(ctx)
	if err != nil {
		t.Fatalf("compute exposure: %v", err)
	}

	entry := exposure.MarketExposure("EXPOSURE-TEST", models.SideBuy)
	if entry.Quantity != result.Quantity {
		t.Errorf("expected quantity %d, got %d", result.Quantity, entry.Quantity)
	}
	if entry.NotionalMicros != result.NotionalMicros {
		t.Errorf("expected notional %d, got %d", result.NotionalMicros, entry.NotionalMicros)
	}
}

func TestExposure_FilledIntentRemovedFromOutstanding(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	l := NewLedger(db)
	limits := permissiveLimits()

	intent := testIntent(uuid.New().String())
	intent.MarketID = "FILL-TEST"
	result, err := l.Append(ctx, intent, limits)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	// Mark as filled
	if err := l.UpdateStatus(ctx, result.IntentID, IntentFilled); err != nil {
		t.Fatalf("update status: %v", err)
	}

	exposure, err := l.ComputeExposure(ctx)
	if err != nil {
		t.Fatalf("compute exposure: %v", err)
	}
	entry := exposure.MarketExposure("FILL-TEST", models.SideBuy)
	if entry.Quantity != 0 {
		t.Errorf("filled intent should not be outstanding, got quantity=%d", entry.Quantity)
	}
}

func TestExposure_CancelledIntentRemovedFromOutstanding(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	l := NewLedger(db)
	limits := permissiveLimits()

	intent := testIntent(uuid.New().String())
	intent.MarketID = "CANCEL-TEST"
	result, err := l.Append(ctx, intent, limits)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	// Mark as cancelled
	if err := l.UpdateStatus(ctx, result.IntentID, IntentCancelled); err != nil {
		t.Fatalf("update status: %v", err)
	}

	exposure, err := l.ComputeExposure(ctx)
	if err != nil {
		t.Fatalf("compute exposure: %v", err)
	}
	entry := exposure.MarketExposure("CANCEL-TEST", models.SideBuy)
	if entry.Quantity != 0 {
		t.Errorf("cancelled intent should not be outstanding, got quantity=%d", entry.Quantity)
	}
}

// ─── Exposure Invariant Enforcement ───

func TestExposureInvariant_WithinLimits(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	l := NewLedger(db)

	intent := testIntent(uuid.New().String())
	limits := ExposureLimits{
		MaxPositionNotionalMicros: 10_000_000, // $10
		MaxTotalExposureMicros:    10_000_000,
	}

	// Intent notional is 5,000,000 ($5) — within $10 limit
	_, err := l.Append(ctx, intent, limits)
	if err != nil {
		t.Fatalf("should have accepted intent within limits: %v", err)
	}
}

func TestExposureInvariant_ExceedsPositionLimit(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	l := NewLedger(db)

	market := "INVARIANT-POS-" + uuid.New().String()[:8]
	limits := ExposureLimits{
		MaxPositionNotionalMicros: 6_000_000, // $6 — just enough for first
		MaxTotalExposureMicros:    100_000_000_000,
	}

	// First intent: 5,000,000 ($5) — accepted
	i1 := testIntent(uuid.New().String())
	i1.MarketID = market
	if _, err := l.Append(ctx, i1, limits); err != nil {
		t.Fatalf("first intent should be accepted: %v", err)
	}

	// Second intent: 5,000,000 ($5) on same market — would make $10, exceeding $6
	i2 := testIntent(uuid.New().String())
	i2.MarketID = market
	_, err := l.Append(ctx, i2, limits)
	if err == nil {
		t.Fatal("second intent should have been rejected for exceeding position limit")
	}
}

func TestExposureInvariant_ExceedsTotalExposure(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	l := NewLedger(db)

	limits := ExposureLimits{
		MaxPositionNotionalMicros: 100_000_000_000,
		MaxTotalExposureMicros:    6_000_000, // $6 total
	}

	// First intent: $5 — accepted
	i1 := testIntent(uuid.New().String())
	if _, err := l.Append(ctx, i1, limits); err != nil {
		t.Fatalf("first intent should be accepted: %v", err)
	}

	// Second intent: $5 on different market — total would be $10, exceeding $6
	i2 := testIntent(uuid.New().String())
	_, err := l.Append(ctx, i2, limits)
	if err == nil {
		t.Fatal("second intent should have been rejected for exceeding total exposure")
	}
}

// ─── Idempotency ───

func TestIdempotency_SameTraceID(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	l := NewLedger(db)
	limits := permissiveLimits()

	traceID := uuid.New().String()

	// First append
	first, err := l.Append(ctx, testIntent(traceID), limits)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}

	// Second append with same trace_id — should return existing
	second, err := l.Append(ctx, testIntent(traceID), limits)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}

	if first.IntentID != second.IntentID {
		t.Errorf("idempotent append should return same intent_id: %s != %s",
			first.IntentID, second.IntentID)
	}
	if first.Version != second.Version {
		t.Errorf("idempotent append should return same version: %d != %d",
			first.Version, second.Version)
	}
}

// ─── Ledger Replay ───

func TestLedgerReplay_ReconstructsExposure(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	l := NewLedger(db)
	limits := permissiveLimits()

	const total = 100
	market := "REPLAY-TEST"
	intentIDs := make([]uuid.UUID, total)

	// Write 100 intents, all to the same market
	for i := 0; i < total; i++ {
		intent := testIntent(uuid.New().String())
		intent.MarketID = market
		result, err := l.Append(ctx, intent, limits)
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		intentIDs[i] = result.IntentID
	}

	// Mark some as filled/cancelled
	for i := 0; i < 30; i++ {
		if err := l.UpdateStatus(ctx, intentIDs[i], IntentFilled); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	for i := 30; i < 50; i++ {
		if err := l.UpdateStatus(ctx, intentIDs[i], IntentCancelled); err != nil {
			t.Fatalf("cancel %d: %v", i, err)
		}
	}

	// Compute live exposure from DB query
	liveExposure, err := l.ComputeExposure(ctx)
	if err != nil {
		t.Fatalf("compute live exposure: %v", err)
	}

	// Replay from full ledger
	intents, err := l.ReplayAll(ctx)
	if err != nil {
		t.Fatalf("replay all: %v", err)
	}
	replayedExposure := ReplayFromIntents(intents)

	// Compare
	if liveExposure.TotalNotionalMicros != replayedExposure.TotalNotionalMicros {
		t.Errorf("total exposure mismatch: live=%d, replayed=%d",
			liveExposure.TotalNotionalMicros, replayedExposure.TotalNotionalMicros)
	}

	// 50 outstanding (indices 50-99), each with notional 5,000,000
	expectedOutstanding := int32(50 * 10) // 50 intents × 10 qty each
	liveEntry := liveExposure.MarketExposure(market, models.SideBuy)
	replayEntry := replayedExposure.MarketExposure(market, models.SideBuy)

	if liveEntry.Quantity != expectedOutstanding {
		t.Errorf("live quantity: expected %d, got %d", expectedOutstanding, liveEntry.Quantity)
	}
	if replayEntry.Quantity != expectedOutstanding {
		t.Errorf("replayed quantity: expected %d, got %d", expectedOutstanding, replayEntry.Quantity)
	}
	if liveEntry.NotionalMicros != replayEntry.NotionalMicros {
		t.Errorf("notional mismatch: live=%d, replayed=%d",
			liveEntry.NotionalMicros, replayEntry.NotionalMicros)
	}

	// Verify version ordering in replay
	for i := 1; i < len(intents); i++ {
		if intents[i].Version <= intents[i-1].Version {
			t.Errorf("replay out of order: version[%d]=%d <= version[%d]=%d",
				i, intents[i].Version, i-1, intents[i-1].Version)
		}
	}
}

// ─── Status Transitions ───

func TestUpdateStatus_TerminalCannotTransition(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	l := NewLedger(db)
	limits := permissiveLimits()

	intent := testIntent(uuid.New().String())
	result, err := l.Append(ctx, intent, limits)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	// Mark as filled (terminal)
	if err := l.UpdateStatus(ctx, result.IntentID, IntentFilled); err != nil {
		t.Fatalf("fill: %v", err)
	}

	// Try to transition again — should fail
	err = l.UpdateStatus(ctx, result.IntentID, IntentCancelled)
	if err == nil {
		t.Fatal("should not be able to transition from terminal state")
	}
}
