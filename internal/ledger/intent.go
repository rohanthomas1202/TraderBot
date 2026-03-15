package ledger

import (
	"fmt"
	"time"

	"autonomy-platform/internal/models"

	"github.com/google/uuid"
)

// IntentStatus tracks the lifecycle of an order intent.
type IntentStatus string

const (
	IntentPending   IntentStatus = "pending"
	IntentOpen      IntentStatus = "open"
	IntentFilled    IntentStatus = "filled"
	IntentCancelled IntentStatus = "cancelled"
	IntentRejected  IntentStatus = "rejected"
	IntentExpired   IntentStatus = "expired"
)

func (s IntentStatus) IsTerminal() bool {
	switch s {
	case IntentFilled, IntentCancelled, IntentRejected, IntentExpired:
		return true
	}
	return false
}

// IsOutstanding returns true if this intent contributes to exposure.
func (s IntentStatus) IsOutstanding() bool {
	return s == IntentPending || s == IntentOpen
}

// OrderIntent represents a single entry in the append-only intent ledger.
// Every proposed order that passes risk approval is recorded here before
// being sent to an exchange.
type OrderIntent struct {
	IntentID      uuid.UUID
	Version       int64 // monotonically increasing, assigned by DB
	TraceID       string
	ApprovalHMAC  []byte
	StrategyID    string
	Venue         string
	MarketID      string
	Side          models.Side
	Quantity      int32
	PriceMicros   int64
	NotionalMicros int64
	Status        IntentStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Validate checks that an OrderIntent has all required fields.
func (i *OrderIntent) Validate() error {
	if i.TraceID == "" {
		return fmt.Errorf("trace_id required")
	}
	if len(i.ApprovalHMAC) == 0 {
		return fmt.Errorf("approval_hmac required")
	}
	if i.StrategyID == "" {
		return fmt.Errorf("strategy_id required")
	}
	if i.MarketID == "" {
		return fmt.Errorf("market_id required")
	}
	if i.Side != models.SideBuy && i.Side != models.SideSell {
		return fmt.Errorf("invalid side: %s", i.Side)
	}
	if i.Quantity <= 0 {
		return fmt.Errorf("quantity must be positive: %d", i.Quantity)
	}
	if i.PriceMicros <= 0 || i.PriceMicros >= 1_000_000 {
		return fmt.Errorf("price must be in (0, 1000000): %d", i.PriceMicros)
	}
	if i.NotionalMicros <= 0 {
		return fmt.Errorf("notional must be positive: %d", i.NotionalMicros)
	}
	return nil
}

// NewIntentFromApproval creates an OrderIntent from a risk-approved order.
func NewIntentFromApproval(order *models.ProposedOrder, approvalHMAC []byte) *OrderIntent {
	return &OrderIntent{
		TraceID:        order.TraceID,
		ApprovalHMAC:   approvalHMAC,
		StrategyID:     order.StrategyID,
		Venue:          order.Venue,
		MarketID:       order.MarketID,
		Side:           order.Side,
		Quantity:        order.Quantity,
		PriceMicros:    order.PriceMicros,
		NotionalMicros: order.NotionalMicros,
		Status:         IntentPending,
	}
}
