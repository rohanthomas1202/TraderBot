//go:build integration

package integration

import (
	"context"
	"testing"

	"autonomy-platform/internal/ledger"
	"autonomy-platform/internal/models"

	"github.com/google/uuid"
)

// TestLedgerReplay verifies that the full ledger can be replayed from the
// database and produces an exposure state identical to the live-computed one.
func TestLedgerReplay(t *testing.T) {
	db, _, _ := setupTestEnv(t)
	ctx := context.Background()

	// Clean slate
	_, err := db.Exec(ctx, "DELETE FROM execution.order_intents")
	if err != nil {
		t.Fatalf("clean intents: %v", err)
	}

	l := ledger.NewLedger(db)
	limits := ledger.ExposureLimits{
		MaxPositionNotionalMicros: 1_000_000_000_000, // very high
		MaxTotalExposureMicros:    1_000_000_000_000,
	}

	const total = 100
	market := "REPLAY-INTEGRATION"
	intentIDs := make([]uuid.UUID, total)

	// Write 100 intents
	for i := 0; i < total; i++ {
		intent := &ledger.OrderIntent{
			TraceID:        uuid.New().String(),
			ApprovalHMAC:   []byte("test-hmac"),
			StrategyID:     "replay-strat",
			Venue:          "mock",
			MarketID:       market,
			Side:           models.SideBuy,
			Quantity:        10,
			PriceMicros:    500_000,
			NotionalMicros: 5_000_000,
			Status:         ledger.IntentPending,
		}
		result, err := l.Append(ctx, intent, limits)
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		intentIDs[i] = result.IntentID
	}

	// Mark 30 as filled, 20 as cancelled
	for i := 0; i < 30; i++ {
		if err := l.UpdateStatus(ctx, intentIDs[i], ledger.IntentFilled); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	for i := 30; i < 50; i++ {
		if err := l.UpdateStatus(ctx, intentIDs[i], ledger.IntentCancelled); err != nil {
			t.Fatalf("cancel %d: %v", i, err)
		}
	}

	// Live exposure from DB
	liveExposure, err := l.ComputeExposure(ctx)
	if err != nil {
		t.Fatalf("compute live: %v", err)
	}

	// Replay from full ledger
	intents, err := l.ReplayAll(ctx)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(intents) != total {
		t.Fatalf("expected %d intents, got %d", total, len(intents))
	}
	replayedExposure := ledger.ReplayFromIntents(intents)

	// Verify totals match
	if liveExposure.TotalNotionalMicros != replayedExposure.TotalNotionalMicros {
		t.Errorf("total mismatch: live=%d, replayed=%d",
			liveExposure.TotalNotionalMicros, replayedExposure.TotalNotionalMicros)
	}

	// 50 outstanding × 5,000,000 = 250,000,000
	expectedNotional := int64(50 * 5_000_000)
	if liveExposure.TotalNotionalMicros != expectedNotional {
		t.Errorf("expected total %d, got %d", expectedNotional, liveExposure.TotalNotionalMicros)
	}

	// Verify per-market match
	liveEntry := liveExposure.MarketExposure(market, models.SideBuy)
	replayEntry := replayedExposure.MarketExposure(market, models.SideBuy)
	if liveEntry.Quantity != replayEntry.Quantity {
		t.Errorf("quantity mismatch: live=%d, replayed=%d", liveEntry.Quantity, replayEntry.Quantity)
	}
	if liveEntry.NotionalMicros != replayEntry.NotionalMicros {
		t.Errorf("notional mismatch: live=%d, replayed=%d",
			liveEntry.NotionalMicros, replayEntry.NotionalMicros)
	}

	// Verify replay is in version order
	for i := 1; i < len(intents); i++ {
		if intents[i].Version <= intents[i-1].Version {
			t.Errorf("replay order violated at index %d: v%d <= v%d",
				i, intents[i].Version, intents[i-1].Version)
		}
	}
}

// TestLedgerIdempotency verifies that the same trace_id submitted twice
// returns the same intent without creating a duplicate.
func TestLedgerIdempotency(t *testing.T) {
	db, _, _ := setupTestEnv(t)
	ctx := context.Background()

	_, err := db.Exec(ctx, "DELETE FROM execution.order_intents")
	if err != nil {
		t.Fatalf("clean intents: %v", err)
	}

	l := ledger.NewLedger(db)
	limits := ledger.ExposureLimits{
		MaxPositionNotionalMicros: 1_000_000_000_000,
		MaxTotalExposureMicros:    1_000_000_000_000,
	}

	traceID := uuid.New().String()
	makeIntent := func() *ledger.OrderIntent {
		return &ledger.OrderIntent{
			TraceID:        traceID,
			ApprovalHMAC:   []byte("test-hmac"),
			StrategyID:     "idempotent-strat",
			Venue:          "mock",
			MarketID:       "IDEMP-MKT",
			Side:           models.SideBuy,
			Quantity:        5,
			PriceMicros:    400_000,
			NotionalMicros: 2_000_000,
			Status:         ledger.IntentPending,
		}
	}

	first, err := l.Append(ctx, makeIntent(), limits)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}

	second, err := l.Append(ctx, makeIntent(), limits)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}

	if first.IntentID != second.IntentID {
		t.Errorf("intent_id mismatch: %s != %s", first.IntentID, second.IntentID)
	}
	if first.Version != second.Version {
		t.Errorf("version mismatch: %d != %d", first.Version, second.Version)
	}

	// Verify only one row in DB
	var count int
	err = db.QueryRow(ctx,
		"SELECT COUNT(*) FROM execution.order_intents WHERE trace_id = $1", traceID).Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row for trace_id, got %d", count)
	}
}

// TestLedgerExposureInvariant verifies that the ledger rejects intents
// that would breach the exposure envelope.
func TestLedgerExposureInvariant(t *testing.T) {
	db, _, _ := setupTestEnv(t)
	ctx := context.Background()

	_, err := db.Exec(ctx, "DELETE FROM execution.order_intents")
	if err != nil {
		t.Fatalf("clean intents: %v", err)
	}

	l := ledger.NewLedger(db)
	market := "INVARIANT-MKT-" + uuid.New().String()[:8]
	limits := ledger.ExposureLimits{
		MaxPositionNotionalMicros: 6_000_000, // $6 per market
		MaxTotalExposureMicros:    100_000_000_000,
	}

	// First intent: $5 — accepted
	i1 := &ledger.OrderIntent{
		TraceID:        uuid.New().String(),
		ApprovalHMAC:   []byte("hmac"),
		StrategyID:     "inv-strat",
		Venue:          "mock",
		MarketID:       market,
		Side:           models.SideBuy,
		Quantity:        10,
		PriceMicros:    500_000,
		NotionalMicros: 5_000_000,
		Status:         ledger.IntentPending,
	}
	if _, err := l.Append(ctx, i1, limits); err != nil {
		t.Fatalf("first intent should be accepted: %v", err)
	}

	// Second intent: $5 on same market — total $10 > $6 limit
	i2 := &ledger.OrderIntent{
		TraceID:        uuid.New().String(),
		ApprovalHMAC:   []byte("hmac"),
		StrategyID:     "inv-strat",
		Venue:          "mock",
		MarketID:       market,
		Side:           models.SideBuy,
		Quantity:        10,
		PriceMicros:    500_000,
		NotionalMicros: 5_000_000,
		Status:         ledger.IntentPending,
	}
	_, err = l.Append(ctx, i2, limits)
	if err == nil {
		t.Fatal("second intent should have been rejected: exceeds position limit")
	}
	t.Logf("correctly rejected: %v", err)
}
