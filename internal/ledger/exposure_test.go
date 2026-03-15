package ledger

import (
	"testing"

	"autonomy-platform/internal/models"
)

func TestExposureState_ApplyAndRemove(t *testing.T) {
	state := NewExposureState()

	intent := &OrderIntent{
		MarketID:       "MKT-1",
		Side:           models.SideBuy,
		Quantity:       10,
		NotionalMicros: 5_000_000,
		Status:         IntentPending,
	}

	state.ApplyIntent(intent)
	if state.TotalNotionalMicros != 5_000_000 {
		t.Errorf("expected total 5000000, got %d", state.TotalNotionalMicros)
	}

	entry := state.MarketExposure("MKT-1", models.SideBuy)
	if entry.Quantity != 10 {
		t.Errorf("expected qty 10, got %d", entry.Quantity)
	}

	state.RemoveIntent(intent)
	if state.TotalNotionalMicros != 0 {
		t.Errorf("expected total 0 after remove, got %d", state.TotalNotionalMicros)
	}
	entry = state.MarketExposure("MKT-1", models.SideBuy)
	if entry.Quantity != 0 {
		t.Errorf("expected qty 0 after remove, got %d", entry.Quantity)
	}
}

func TestExposureState_SkipsTerminalIntents(t *testing.T) {
	state := NewExposureState()

	filled := &OrderIntent{
		MarketID:       "MKT-1",
		Side:           models.SideBuy,
		Quantity:       10,
		NotionalMicros: 5_000_000,
		Status:         IntentFilled,
	}

	state.ApplyIntent(filled)
	if state.TotalNotionalMicros != 0 {
		t.Errorf("terminal intent should not add exposure, got %d", state.TotalNotionalMicros)
	}
}

func TestCheckInvariant_WithinLimits(t *testing.T) {
	state := NewExposureState()
	intent := &OrderIntent{
		MarketID:       "MKT-1",
		Side:           models.SideBuy,
		Quantity:       10,
		NotionalMicros: 5_000_000,
	}
	limits := ExposureLimits{
		MaxPositionNotionalMicros: 10_000_000,
		MaxTotalExposureMicros:    10_000_000,
	}

	if err := state.CheckInvariant(intent, limits); err != nil {
		t.Errorf("should be within limits: %v", err)
	}
}

func TestCheckInvariant_ExceedsPosition(t *testing.T) {
	state := NewExposureState()
	// Pre-load some exposure
	state.Outstanding[MarketSideKey{MarketID: "MKT-1", Side: models.SideBuy}] = ExposureEntry{
		Quantity:       10,
		NotionalMicros: 6_000_000,
	}
	state.TotalNotionalMicros = 6_000_000

	intent := &OrderIntent{
		MarketID:       "MKT-1",
		Side:           models.SideBuy,
		Quantity:       10,
		NotionalMicros: 5_000_000,
	}
	limits := ExposureLimits{
		MaxPositionNotionalMicros: 10_000_000, // 6M + 5M = 11M > 10M
		MaxTotalExposureMicros:    100_000_000,
	}

	if err := state.CheckInvariant(intent, limits); err == nil {
		t.Error("should have rejected: position limit exceeded")
	}
}

func TestCheckInvariant_ExceedsTotal(t *testing.T) {
	state := NewExposureState()
	state.Outstanding[MarketSideKey{MarketID: "MKT-1", Side: models.SideBuy}] = ExposureEntry{
		Quantity:       10,
		NotionalMicros: 6_000_000,
	}
	state.TotalNotionalMicros = 6_000_000

	intent := &OrderIntent{
		MarketID:       "MKT-2", // different market, so position ok
		Side:           models.SideBuy,
		Quantity:       10,
		NotionalMicros: 5_000_000,
	}
	limits := ExposureLimits{
		MaxPositionNotionalMicros: 100_000_000,
		MaxTotalExposureMicros:    10_000_000, // 6M + 5M = 11M > 10M
	}

	if err := state.CheckInvariant(intent, limits); err == nil {
		t.Error("should have rejected: total exposure limit exceeded")
	}
}

func TestCheckInvariant_ZeroLimitsMeansNoLimit(t *testing.T) {
	state := NewExposureState()
	state.TotalNotionalMicros = 999_999_999_999

	intent := &OrderIntent{
		MarketID:       "MKT-1",
		Side:           models.SideBuy,
		Quantity:       10,
		NotionalMicros: 5_000_000,
	}
	limits := ExposureLimits{} // zero = no limit

	if err := state.CheckInvariant(intent, limits); err != nil {
		t.Errorf("zero limits should mean no restriction: %v", err)
	}
}

func TestReplayFromIntents(t *testing.T) {
	intents := []*OrderIntent{
		{MarketID: "MKT-1", Side: models.SideBuy, Quantity: 10, NotionalMicros: 5_000_000, Status: IntentPending},
		{MarketID: "MKT-1", Side: models.SideBuy, Quantity: 5, NotionalMicros: 2_500_000, Status: IntentOpen},
		{MarketID: "MKT-1", Side: models.SideBuy, Quantity: 10, NotionalMicros: 5_000_000, Status: IntentFilled},    // terminal
		{MarketID: "MKT-1", Side: models.SideSell, Quantity: 3, NotionalMicros: 1_500_000, Status: IntentPending},
		{MarketID: "MKT-2", Side: models.SideBuy, Quantity: 20, NotionalMicros: 10_000_000, Status: IntentCancelled}, // terminal
	}

	state := ReplayFromIntents(intents)

	// MKT-1 buy: 10 (pending) + 5 (open) = 15 qty, 7.5M notional
	buyEntry := state.MarketExposure("MKT-1", models.SideBuy)
	if buyEntry.Quantity != 15 {
		t.Errorf("MKT-1 buy qty: expected 15, got %d", buyEntry.Quantity)
	}
	if buyEntry.NotionalMicros != 7_500_000 {
		t.Errorf("MKT-1 buy notional: expected 7500000, got %d", buyEntry.NotionalMicros)
	}

	// MKT-1 sell: 3 qty, 1.5M notional
	sellEntry := state.MarketExposure("MKT-1", models.SideSell)
	if sellEntry.Quantity != 3 {
		t.Errorf("MKT-1 sell qty: expected 3, got %d", sellEntry.Quantity)
	}

	// MKT-2 buy: cancelled, should be 0
	mkt2Entry := state.MarketExposure("MKT-2", models.SideBuy)
	if mkt2Entry.Quantity != 0 {
		t.Errorf("MKT-2 buy should be 0 (cancelled), got %d", mkt2Entry.Quantity)
	}

	// Total: 7.5M + 1.5M = 9M
	if state.TotalNotionalMicros != 9_000_000 {
		t.Errorf("total: expected 9000000, got %d", state.TotalNotionalMicros)
	}
}
