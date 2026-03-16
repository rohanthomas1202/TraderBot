package dashboard

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OrderRow represents an order from the execution.orders table.
type OrderRow struct {
	InternalOrderID string     `json:"internal_order_id"`
	TraceID         string     `json:"trace_id"`
	StrategyID      string     `json:"strategy_id"`
	Venue           string     `json:"venue"`
	MarketID        string     `json:"market_id"`
	Side            string     `json:"side"`
	Quantity        int32      `json:"quantity"`
	PriceMicros     int64      `json:"price_micros"`
	NotionalMicros  int64      `json:"notional_micros"`
	Status          string     `json:"status"`
	FilledQuantity  int32      `json:"filled_quantity"`
	ExchangeOrderID string     `json:"exchange_order_id"`
	ProposedAt      time.Time  `json:"proposed_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

// PositionRow represents a position from risk.positions.
type PositionRow struct {
	Venue        string `json:"venue"`
	MarketID     string `json:"market_id"`
	StrategyID   string `json:"strategy_id"`
	NetQuantity  int32  `json:"net_quantity"`
	NotionalMicros int64 `json:"notional_micros"`
}

// AuditEntry represents a row from audit.event_log.
type AuditEntry struct {
	ID        int64     `json:"id"`
	Service   string    `json:"service"`
	EventType string    `json:"event_type"`
	TraceID   string    `json:"trace_id"`
	Severity  string    `json:"severity"`
	Timestamp time.Time `json:"timestamp"`
}

// HaltRow represents an active halt from watchdog.kill_switch_events.
type HaltRow struct {
	Level       string    `json:"level"`
	Scope       string    `json:"scope"`
	Reason      string    `json:"reason"`
	TriggeredBy string    `json:"triggered_by"`
	TriggeredAt time.Time `json:"triggered_at"`
	Acknowledged bool    `json:"acknowledged"`
}

// RiskStats holds aggregate risk stats.
type RiskStats struct {
	DailyPnlMicros      int64 `json:"daily_pnl_micros"`
	TotalTurnoverMicros  int64 `json:"total_turnover_micros"`
	FillCount            int32 `json:"fill_count"`
	OrderCount           int32 `json:"order_count"`
}

// QueryOpenOrders returns orders in non-terminal states.
func QueryOpenOrders(ctx context.Context, db *pgxpool.Pool) ([]OrderRow, error) {
	rows, err := db.Query(ctx,
		`SELECT internal_order_id, trace_id, strategy_id, venue, market_id, side,
		        quantity, price_micros, notional_micros, status, filled_quantity,
		        COALESCE(exchange_order_id, ''), proposed_at, completed_at
		 FROM execution.orders
		 WHERE status IN ('pending', 'open', 'partially_filled')
		 ORDER BY proposed_at DESC
		 LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []OrderRow
	for rows.Next() {
		var o OrderRow
		if err := rows.Scan(&o.InternalOrderID, &o.TraceID, &o.StrategyID,
			&o.Venue, &o.MarketID, &o.Side, &o.Quantity, &o.PriceMicros,
			&o.NotionalMicros, &o.Status, &o.FilledQuantity,
			&o.ExchangeOrderID, &o.ProposedAt, &o.CompletedAt); err != nil {
			return nil, err
		}
		result = append(result, o)
	}
	return result, nil
}

// QueryRecentOrders returns the most recent orders regardless of status.
func QueryRecentOrders(ctx context.Context, db *pgxpool.Pool, limit int) ([]OrderRow, error) {
	rows, err := db.Query(ctx,
		`SELECT internal_order_id, trace_id, strategy_id, venue, market_id, side,
		        quantity, price_micros, notional_micros, status, filled_quantity,
		        COALESCE(exchange_order_id, ''), proposed_at, completed_at
		 FROM execution.orders
		 ORDER BY proposed_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []OrderRow
	for rows.Next() {
		var o OrderRow
		if err := rows.Scan(&o.InternalOrderID, &o.TraceID, &o.StrategyID,
			&o.Venue, &o.MarketID, &o.Side, &o.Quantity, &o.PriceMicros,
			&o.NotionalMicros, &o.Status, &o.FilledQuantity,
			&o.ExchangeOrderID, &o.ProposedAt, &o.CompletedAt); err != nil {
			return nil, err
		}
		result = append(result, o)
	}
	return result, nil
}

// QueryPositions returns all non-zero positions.
func QueryPositions(ctx context.Context, db *pgxpool.Pool) ([]PositionRow, error) {
	rows, err := db.Query(ctx,
		`SELECT venue, market_id, strategy_id, net_quantity, notional_micros
		 FROM risk.positions WHERE net_quantity != 0
		 ORDER BY notional_micros DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []PositionRow
	for rows.Next() {
		var p PositionRow
		if err := rows.Scan(&p.Venue, &p.MarketID, &p.StrategyID, &p.NetQuantity, &p.NotionalMicros); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, nil
}

// QueryRecentAudit returns the most recent audit log entries.
func QueryRecentAudit(ctx context.Context, db *pgxpool.Pool, limit int) ([]AuditEntry, error) {
	rows, err := db.Query(ctx,
		`SELECT id, service, event_type, COALESCE(trace_id, ''), severity, timestamp
		 FROM audit.event_log
		 ORDER BY id DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []AuditEntry
	for rows.Next() {
		var a AuditEntry
		if err := rows.Scan(&a.ID, &a.Service, &a.EventType, &a.TraceID, &a.Severity, &a.Timestamp); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, nil
}

// QueryActiveHalts returns all unresolved kill switch events.
func QueryActiveHalts(ctx context.Context, db *pgxpool.Pool) ([]HaltRow, error) {
	rows, err := db.Query(ctx,
		`SELECT level, scope, reason, triggered_by, triggered_at, acknowledged
		 FROM watchdog.kill_switch_events
		 WHERE resumed = FALSE
		 ORDER BY triggered_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []HaltRow
	for rows.Next() {
		var h HaltRow
		if err := rows.Scan(&h.Level, &h.Scope, &h.Reason, &h.TriggeredBy, &h.TriggeredAt, &h.Acknowledged); err != nil {
			return nil, err
		}
		result = append(result, h)
	}
	return result, nil
}

// QueryRiskStats returns today's risk statistics.
func QueryRiskStats(ctx context.Context, db *pgxpool.Pool) (*RiskStats, error) {
	var stats RiskStats
	err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(pnl_micros), 0), COALESCE(SUM(turnover_micros), 0),
		        COALESCE(SUM(fill_count), 0), COALESCE(SUM(order_count), 0)
		 FROM risk.daily_stats
		 WHERE date = CURRENT_DATE AND scope = 'global'`).
		Scan(&stats.DailyPnlMicros, &stats.TotalTurnoverMicros, &stats.FillCount, &stats.OrderCount)
	if err != nil {
		return nil, err
	}
	return &stats, nil
}
