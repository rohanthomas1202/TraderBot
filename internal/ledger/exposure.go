package ledger

import (
	"fmt"

	"autonomy-platform/internal/models"
)

// MarketSideKey uniquely identifies one side of exposure in a market.
type MarketSideKey struct {
	MarketID string
	Side     models.Side
}

// ExposureEntry holds the aggregate outstanding exposure for one market+side.
type ExposureEntry struct {
	Quantity       int32
	NotionalMicros int64
}

// ExposureState holds the full exposure picture derived from outstanding intents.
type ExposureState struct {
	Outstanding         map[MarketSideKey]ExposureEntry
	TotalNotionalMicros int64
}

// ExposureLimits defines the envelope that intents must not exceed.
type ExposureLimits struct {
	MaxPositionNotionalMicros int64 // per-market per-side
	MaxTotalExposureMicros    int64 // global
}

func NewExposureState() *ExposureState {
	return &ExposureState{
		Outstanding: make(map[MarketSideKey]ExposureEntry),
	}
}

// CheckInvariant verifies that adding the given intent would not breach limits.
func (e *ExposureState) CheckInvariant(intent *OrderIntent, limits ExposureLimits) error {
	key := MarketSideKey{MarketID: intent.MarketID, Side: intent.Side}
	current := e.Outstanding[key]

	newNotional := current.NotionalMicros + intent.NotionalMicros
	if limits.MaxPositionNotionalMicros > 0 && newNotional > limits.MaxPositionNotionalMicros {
		return fmt.Errorf(
			"market %s %s exposure would be %d, exceeds limit %d",
			intent.MarketID, intent.Side, newNotional, limits.MaxPositionNotionalMicros,
		)
	}

	newTotal := e.TotalNotionalMicros + intent.NotionalMicros
	if limits.MaxTotalExposureMicros > 0 && newTotal > limits.MaxTotalExposureMicros {
		return fmt.Errorf(
			"total exposure would be %d, exceeds limit %d",
			newTotal, limits.MaxTotalExposureMicros,
		)
	}

	return nil
}

// ApplyIntent adds an intent's exposure to the state. Used during replay.
func (e *ExposureState) ApplyIntent(intent *OrderIntent) {
	if !intent.Status.IsOutstanding() {
		return
	}
	key := MarketSideKey{MarketID: intent.MarketID, Side: intent.Side}
	entry := e.Outstanding[key]
	entry.Quantity += intent.Quantity
	entry.NotionalMicros += intent.NotionalMicros
	e.Outstanding[key] = entry
	e.TotalNotionalMicros += intent.NotionalMicros
}

// RemoveIntent removes an intent's exposure from the state. Used when an
// intent transitions to a terminal status.
func (e *ExposureState) RemoveIntent(intent *OrderIntent) {
	key := MarketSideKey{MarketID: intent.MarketID, Side: intent.Side}
	entry, exists := e.Outstanding[key]
	if !exists {
		return
	}
	entry.Quantity -= intent.Quantity
	entry.NotionalMicros -= intent.NotionalMicros
	e.TotalNotionalMicros -= intent.NotionalMicros
	if entry.Quantity <= 0 {
		delete(e.Outstanding, key)
	} else {
		e.Outstanding[key] = entry
	}
}

// ReplayFromIntents reconstructs the full exposure state from a list of intents
// (typically the full ledger in version order).
func ReplayFromIntents(intents []*OrderIntent) *ExposureState {
	state := NewExposureState()
	for _, intent := range intents {
		state.ApplyIntent(intent)
	}
	return state
}

// MarketExposure returns the outstanding exposure for a specific market+side.
func (e *ExposureState) MarketExposure(marketID string, side models.Side) ExposureEntry {
	return e.Outstanding[MarketSideKey{MarketID: marketID, Side: side}]
}
