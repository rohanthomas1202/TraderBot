package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

type OrderStatus string

const (
	StatusPending         OrderStatus = "pending"
	StatusOpen            OrderStatus = "open"
	StatusPartiallyFilled OrderStatus = "partially_filled"
	StatusFilled          OrderStatus = "filled"
	StatusCancelled       OrderStatus = "cancelled"
	StatusRejected        OrderStatus = "rejected"
	StatusExpired         OrderStatus = "expired"
	StatusFailed          OrderStatus = "failed"
)

func (s OrderStatus) IsTerminal() bool {
	switch s {
	case StatusFilled, StatusCancelled, StatusRejected, StatusExpired, StatusFailed:
		return true
	}
	return false
}

// validTransitions defines every legal state transition.
var validTransitions = map[OrderStatus][]OrderStatus{
	StatusPending:         {StatusOpen, StatusRejected, StatusFailed, StatusFilled},
	StatusOpen:            {StatusPartiallyFilled, StatusFilled, StatusCancelled, StatusExpired},
	StatusPartiallyFilled: {StatusPartiallyFilled, StatusFilled, StatusCancelled},
}

func ValidateTransition(from, to OrderStatus) error {
	allowed, exists := validTransitions[from]
	if !exists {
		return fmt.Errorf("no transitions from terminal state: %s", from)
	}
	for _, a := range allowed {
		if a == to {
			return nil
		}
	}
	return fmt.Errorf("illegal transition: %s → %s", from, to)
}

type ProposedOrder struct {
	TraceID       string
	StrategyID    string
	Venue         string
	MarketID      string
	Side          Side
	Quantity      int32
	PriceMicros   int64 // 0–1,000,000 for prediction markets
	NotionalMicros int64
	ProposedAt    time.Time
}

func (o *ProposedOrder) Validate() error {
	if o.TraceID == "" {
		return fmt.Errorf("trace_id required")
	}
	if o.StrategyID == "" {
		return fmt.Errorf("strategy_id required")
	}
	if o.MarketID == "" {
		return fmt.Errorf("market_id required")
	}
	if o.Side != SideBuy && o.Side != SideSell {
		return fmt.Errorf("invalid side: %s", o.Side)
	}
	if o.Quantity <= 0 {
		return fmt.Errorf("quantity must be positive: %d", o.Quantity)
	}
	if o.PriceMicros <= 0 || o.PriceMicros >= 1_000_000 {
		return fmt.Errorf("price must be in (0, 1000000): %d", o.PriceMicros)
	}
	if o.NotionalMicros <= 0 {
		return fmt.Errorf("notional must be positive: %d", o.NotionalMicros)
	}
	expected := int64(o.Quantity) * o.PriceMicros
	if o.NotionalMicros != expected {
		return fmt.Errorf("notional mismatch: %d != quantity(%d) * price(%d) = %d",
			o.NotionalMicros, o.Quantity, o.PriceMicros, expected)
	}
	return nil
}

func (o *ProposedOrder) IdempotencyKey() string {
	return fmt.Sprintf("%s:%s:%s:%s:%d", o.StrategyID, o.Venue, o.MarketID, o.Side, o.PriceMicros)
}

type OrderRecord struct {
	InternalOrderID   uuid.UUID
	TraceID           string
	StrategyID        string
	Venue             string
	MarketID          string
	Side              Side
	Quantity          int32
	PriceMicros       int64
	NotionalMicros    int64
	ExchangeOrderID   string
	Status            OrderStatus
	FilledQuantity    int32
	AvgFillPriceMicros int64
	CredentialID      string
	SigningKeyID      string
	ApprovalHMAC      []byte
	PolicyConfigHash  string
	ProposedAt        time.Time
	ApprovedAt        time.Time
	SubmittedAt       *time.Time
	AcknowledgedAt    *time.Time
	LastFillAt        *time.Time
	CompletedAt       *time.Time
	CreatedAt         time.Time
}

type Fill struct {
	FillID          uuid.UUID
	InternalOrderID uuid.UUID
	ExchangeFillID  string
	Quantity        int32
	PriceMicros     int64
	FeeMicros       int64
	FilledAt        time.Time
}
