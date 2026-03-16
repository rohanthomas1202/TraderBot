package alertbot

import (
	"fmt"
	"time"

	"autonomy-platform/internal/events"
	"autonomy-platform/services/dashboard"
)

func micros(v int64) string {
	return fmt.Sprintf("$%.2f", float64(v)/1_000_000)
}

// FormatStatus formats system status for Telegram.
func FormatStatus(mode string, halts []dashboard.HaltRow) string {
	msg := fmt.Sprintf("System Status\nMode: %s\n", mode)
	if len(halts) == 0 {
		msg += "Halts: none\n"
	} else {
		msg += fmt.Sprintf("Active Halts: %d\n", len(halts))
		for _, h := range halts {
			msg += fmt.Sprintf("  - %s (%s): %s\n", h.Level, h.Scope, h.Reason)
		}
	}
	return msg
}

// FormatRiskStats formats risk statistics for Telegram.
func FormatRiskStats(stats *dashboard.RiskStats) string {
	return fmt.Sprintf("P&L Report\nDaily P&L: %s\nTurnover: %s\nFills: %d\nOrders: %d",
		micros(stats.DailyPnlMicros),
		micros(stats.TotalTurnoverMicros),
		stats.FillCount,
		stats.OrderCount,
	)
}

// FormatOrders formats recent orders for Telegram.
func FormatOrders(orders []dashboard.OrderRow) string {
	if len(orders) == 0 {
		return "Orders\nNo open orders"
	}
	msg := "Open Orders\n"
	for i, o := range orders {
		if i >= 10 {
			msg += fmt.Sprintf("... and %d more\n", len(orders)-10)
			break
		}
		msg += fmt.Sprintf("%s %s %d @ %s [%s]\n",
			o.MarketID, o.Side, o.Quantity, micros(o.PriceMicros), o.Status)
	}
	return msg
}

// FormatOrderProposed formats an order proposal notification.
func FormatOrderProposed(e events.OrderProposedEvent) string {
	return fmt.Sprintf("\U0001F7E1 Order Proposed\n%s %s %d @ %s\nStrategy: %s\nMarket: %s\nTrace: %s",
		e.Side, e.Venue, e.Quantity, micros(e.PriceMicros),
		e.StrategyID, e.MarketID, e.TraceID[:8])
}

// FormatOrderApproved formats an order approval notification.
func FormatOrderApproved(e events.OrderApprovedEvent) string {
	return fmt.Sprintf("\u2705 Order Approved\nTrace: %s", e.TraceID[:8])
}

// FormatOrderDenied formats an order denial notification.
func FormatOrderDenied(e events.OrderDeniedEvent) string {
	msg := fmt.Sprintf("\u274C Order DENIED\nReason: %s\nTrace: %s", e.DenyReason, e.TraceID[:8])
	if len(e.FailedChecks) > 0 {
		msg += "\nFailed checks:"
		for _, c := range e.FailedChecks {
			msg += fmt.Sprintf("\n  - %s: %s", c.Name, c.Detail)
		}
	}
	return msg
}

// FormatOrderSubmitted formats an order submission notification.
func FormatOrderSubmitted(e events.OrderSubmittedEvent) string {
	return fmt.Sprintf("\U0001F4E4 Order Submitted\nVenue: %s\nExchange ID: %s\nTrace: %s",
		e.Venue, e.ExchangeOrderID, e.TraceID[:8])
}

// FormatFillNotification formats a fill event for push notification.
func FormatFillNotification(e events.OrderFilledEvent) string {
	return fmt.Sprintf("\U0001F4B0 Fill: %d @ %s (remaining: %d)\nTrace: %s",
		e.FilledQuantity, micros(e.AveragePriceMicros), e.RemainingQuantity,
		e.TraceID[:8])
}

// FormatKillSwitchNotification formats a kill switch event.
func FormatKillSwitchNotification(e events.KillSwitchEvent) string {
	return fmt.Sprintf("\U0001F6A8 KILL SWITCH\nLevel: %s\nScope: %s\nReason: %s\nBy: %s\nTime: %s",
		e.Level, e.Scope, e.Reason, e.TriggeredBy,
		e.Timestamp.Format(time.RFC3339))
}
