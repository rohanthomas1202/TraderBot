package convert

import (
	"time"

	"autonomy-platform/gen/commonpb"
	"autonomy-platform/gen/executionpb"
	"autonomy-platform/gen/riskpb"
	"autonomy-platform/gen/watchdogpb"
	"autonomy-platform/internal/models"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─── Side ───

func SideToProto(s models.Side) commonpb.Side {
	switch s {
	case models.SideBuy:
		return commonpb.Side_SIDE_BUY
	case models.SideSell:
		return commonpb.Side_SIDE_SELL
	default:
		return commonpb.Side_SIDE_UNSPECIFIED
	}
}

func SideFromProto(s commonpb.Side) models.Side {
	switch s {
	case commonpb.Side_SIDE_BUY:
		return models.SideBuy
	case commonpb.Side_SIDE_SELL:
		return models.SideSell
	default:
		return ""
	}
}

// ─── Venue ───

func VenueToProto(v string) commonpb.Venue {
	switch v {
	case "kalshi":
		return commonpb.Venue_VENUE_KALSHI
	case "polymarket":
		return commonpb.Venue_VENUE_POLYMARKET
	case "mock":
		return commonpb.Venue_VENUE_MOCK
	default:
		return commonpb.Venue_VENUE_UNSPECIFIED
	}
}

func VenueFromProto(v commonpb.Venue) string {
	switch v {
	case commonpb.Venue_VENUE_KALSHI:
		return "kalshi"
	case commonpb.Venue_VENUE_POLYMARKET:
		return "polymarket"
	case commonpb.Venue_VENUE_MOCK:
		return "mock"
	default:
		return ""
	}
}

// ─── ProposedOrder ───

func ProposedOrderToProto(o *models.ProposedOrder) *riskpb.ProposedOrder {
	return &riskpb.ProposedOrder{
		TraceId:        o.TraceID,
		StrategyId:     o.StrategyID,
		Market:         &commonpb.Market{Venue: VenueToProto(o.Venue), MarketId: o.MarketID},
		Side:           SideToProto(o.Side),
		OrderType:      commonpb.OrderType_ORDER_TYPE_LIMIT,
		Quantity:       o.Quantity,
		PriceMicros:    o.PriceMicros,
		NotionalMicros: o.NotionalMicros,
		ProposedAt:     timestamppb.New(o.ProposedAt),
	}
}

func ProposedOrderFromProto(p *riskpb.ProposedOrder) *models.ProposedOrder {
	var venue, marketID string
	if p.GetMarket() != nil {
		venue = VenueFromProto(p.GetMarket().GetVenue())
		marketID = p.GetMarket().GetMarketId()
	}
	var proposedAt time.Time
	if p.GetProposedAt() != nil {
		proposedAt = p.GetProposedAt().AsTime()
	}
	return &models.ProposedOrder{
		TraceID:        p.GetTraceId(),
		StrategyID:     p.GetStrategyId(),
		Venue:          venue,
		MarketID:       marketID,
		Side:           SideFromProto(p.GetSide()),
		Quantity:       p.GetQuantity(),
		PriceMicros:    p.GetPriceMicros(),
		NotionalMicros: p.GetNotionalMicros(),
		ProposedAt:     proposedAt,
	}
}

// ─── OrderRecord ───

func OrderRecordToProto(r *models.OrderRecord) *executionpb.OrderRecord {
	rec := &executionpb.OrderRecord{
		TraceId:            r.TraceID,
		InternalOrderId:    r.InternalOrderID.String(),
		ExchangeOrderId:    r.ExchangeOrderID,
		Status:             OrderStatusToProto(r.Status),
		FilledQuantity:     r.FilledQuantity,
		AvgFillPriceMicros: r.AvgFillPriceMicros,
		CredentialId:       r.CredentialID,
		SigningKeyId:       r.SigningKeyID,
		Proposed: &riskpb.ProposedOrder{
			TraceId:        r.TraceID,
			StrategyId:     r.StrategyID,
			Market:         &commonpb.Market{Venue: VenueToProto(r.Venue), MarketId: r.MarketID},
			Side:           SideToProto(r.Side),
			OrderType:      commonpb.OrderType_ORDER_TYPE_LIMIT,
			Quantity:       r.Quantity,
			PriceMicros:    r.PriceMicros,
			NotionalMicros: r.NotionalMicros,
			ProposedAt:     timestamppb.New(r.ProposedAt),
		},
	}
	if r.SubmittedAt != nil {
		rec.SubmittedAt = timestamppb.New(*r.SubmittedAt)
	}
	return rec
}

func OrderStatusToProto(s models.OrderStatus) executionpb.OrderStatus {
	switch s {
	case models.StatusPending:
		return executionpb.OrderStatus_ORDER_STATUS_PENDING
	case models.StatusOpen:
		return executionpb.OrderStatus_ORDER_STATUS_OPEN
	case models.StatusPartiallyFilled:
		return executionpb.OrderStatus_ORDER_STATUS_PARTIALLY_FILLED
	case models.StatusFilled:
		return executionpb.OrderStatus_ORDER_STATUS_FILLED
	case models.StatusCancelled:
		return executionpb.OrderStatus_ORDER_STATUS_CANCELLED
	case models.StatusRejected:
		return executionpb.OrderStatus_ORDER_STATUS_REJECTED
	case models.StatusExpired:
		return executionpb.OrderStatus_ORDER_STATUS_EXPIRED
	case models.StatusFailed:
		return executionpb.OrderStatus_ORDER_STATUS_FAILED
	default:
		return executionpb.OrderStatus_ORDER_STATUS_UNSPECIFIED
	}
}

// ─── Kill Switch Level ───

func KillSwitchLevelFromProto(l watchdogpb.KillSwitchLevel) string {
	switch l {
	case watchdogpb.KillSwitchLevel_KILL_LEVEL_SOFT_PAUSE:
		return "soft_pause"
	case watchdogpb.KillSwitchLevel_KILL_LEVEL_CANCEL_ONLY:
		return "cancel_only"
	case watchdogpb.KillSwitchLevel_KILL_LEVEL_FULL_STOP:
		return "full_stop"
	default:
		return ""
	}
}

func KillSwitchLevelToProto(l string) watchdogpb.KillSwitchLevel {
	switch l {
	case "soft_pause":
		return watchdogpb.KillSwitchLevel_KILL_LEVEL_SOFT_PAUSE
	case "cancel_only":
		return watchdogpb.KillSwitchLevel_KILL_LEVEL_CANCEL_ONLY
	case "full_stop":
		return watchdogpb.KillSwitchLevel_KILL_LEVEL_FULL_STOP
	default:
		return watchdogpb.KillSwitchLevel_KILL_LEVEL_UNSPECIFIED
	}
}
