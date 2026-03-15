package events

import "time"

// Subject naming convention: {domain}.{action}.{scope}
// Examples: order.proposed.momentum-v1, risk.breach.global, kill.activated.global

// ─── Subjects ───

const (
	SubjectMarketData     = "data.market"            // data.market.{venue}.{market_id}
	SubjectOrderProposed  = "order.proposed"          // order.proposed.{strategy_id}
	SubjectOrderApproved  = "order.approved"          // order.approved.{strategy_id}
	SubjectOrderDenied    = "order.denied"            // order.denied.{strategy_id}
	SubjectOrderSubmitted = "order.submitted"         // order.submitted.{venue}
	SubjectOrderFilled    = "order.filled"            // order.filled.{venue}
	SubjectOrderCancelled = "order.cancelled"         // order.cancelled.{venue}
	SubjectRiskBreach     = "risk.breach"             // risk.breach.{scope}
	SubjectKillActivated  = "kill.activated"          // kill.activated.{scope}
	SubjectKillAcked      = "kill.acknowledged"       // kill.acknowledged.{scope}
	SubjectHeartbeat      = "system.heartbeat"        // system.heartbeat.{service}
)

// ─── Event Payloads ───

type MarketDataEvent struct {
	Venue          string    `json:"venue"`
	MarketID       string    `json:"market_id"`
	BidPriceMicros int64     `json:"bid_price_micros"`
	AskPriceMicros int64     `json:"ask_price_micros"`
	LastPriceMicros int64    `json:"last_price_micros"`
	BidDepth       int32     `json:"bid_depth"`
	AskDepth       int32     `json:"ask_depth"`
	Timestamp      time.Time `json:"timestamp"`
}

type OrderProposedEvent struct {
	TraceID        string    `json:"trace_id"`
	StrategyID     string    `json:"strategy_id"`
	Venue          string    `json:"venue"`
	MarketID       string    `json:"market_id"`
	Side           string    `json:"side"`
	Quantity       int32     `json:"quantity"`
	PriceMicros    int64     `json:"price_micros"`
	NotionalMicros int64     `json:"notional_micros"`
	Timestamp      time.Time `json:"timestamp"`
}

type OrderApprovedEvent struct {
	TraceID          string    `json:"trace_id"`
	PolicyConfigHash string    `json:"policy_config_hash"`
	Timestamp        time.Time `json:"timestamp"`
}

type OrderDeniedEvent struct {
	TraceID      string         `json:"trace_id"`
	DenyReason   string         `json:"deny_reason"`
	FailedChecks []CheckResult  `json:"failed_checks"`
	Timestamp    time.Time      `json:"timestamp"`
}

type CheckResult struct {
	Name   string `json:"name"`
	Result string `json:"result"` // "pass", "fail", "skip"
	Detail string `json:"detail,omitempty"`
}

type OrderSubmittedEvent struct {
	TraceID         string    `json:"trace_id"`
	InternalOrderID string    `json:"internal_order_id"`
	ExchangeOrderID string    `json:"exchange_order_id"`
	CredentialID    string    `json:"credential_id"`
	Venue           string    `json:"venue"`
	Timestamp       time.Time `json:"timestamp"`
}

type OrderFilledEvent struct {
	TraceID            string    `json:"trace_id"`
	InternalOrderID    string    `json:"internal_order_id"`
	ExchangeOrderID    string    `json:"exchange_order_id"`
	FilledQuantity     int32     `json:"filled_quantity"`
	AveragePriceMicros int64     `json:"average_price_micros"`
	RemainingQuantity  int32     `json:"remaining_quantity"`
	Timestamp          time.Time `json:"timestamp"`
}

type OrderCancelledEvent struct {
	TraceID         string    `json:"trace_id"`
	InternalOrderID string    `json:"internal_order_id"`
	Reason          string    `json:"reason"`
	CancelledBy     string    `json:"cancelled_by"`
	Timestamp       time.Time `json:"timestamp"`
}

type RiskBreachEvent struct {
	TraceID     string    `json:"trace_id,omitempty"`
	LimitName   string    `json:"limit_name"`
	LimitValue  int64     `json:"limit_value_micros"`
	ActualValue int64     `json:"actual_value_micros"`
	Scope       string    `json:"scope"`
	Action      string    `json:"action"`
	Timestamp   time.Time `json:"timestamp"`
}

type KillSwitchEvent struct {
	Level       string    `json:"level"`
	Scope       string    `json:"scope"`
	Reason      string    `json:"reason"`
	TriggeredBy string    `json:"triggered_by"`
	Timestamp   time.Time `json:"timestamp"`
}

type HeartbeatEvent struct {
	Service   string    `json:"service"`
	Status    string    `json:"status"` // "healthy", "degraded"
	Detail    string    `json:"detail,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}
