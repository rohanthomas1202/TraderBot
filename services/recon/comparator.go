package recon

import (
	"context"
	"fmt"
	"log/slog"

	"autonomy-platform/services/execution"
	"autonomy-platform/services/mockexchange"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Comparator compares internal state (PostgreSQL) against exchange state
// (mock exchange) and returns discrepancies.
type Comparator struct {
	db     *pgxpool.Pool
	venue  *execution.PaperAdapter
	logger *slog.Logger
}

func NewComparator(db *pgxpool.Pool, venue *execution.PaperAdapter) *Comparator {
	return &Comparator{
		db:     db,
		venue:  venue,
		logger: slog.Default().With("component", "recon-comparator"),
	}
}

// PositionSnapshot captures position state from one source.
type PositionSnapshot struct {
	MarketID    string `json:"market_id"`
	NetQuantity int32  `json:"net_quantity"`
}

// OrderSnapshot captures order state from one source.
type OrderSnapshot struct {
	ExchangeOrderID string `json:"exchange_order_id"`
	InternalOrderID string `json:"internal_order_id"`
	Status          string `json:"status"`
	FilledQuantity  int32  `json:"filled_quantity"`
	Quantity        int32  `json:"quantity"`
}

// BalanceSnapshot captures balance from the exchange.
type BalanceSnapshot struct {
	BalanceMicros int64 `json:"balance_micros"`
}

// Mismatch describes a single discrepancy between internal and exchange state.
type Mismatch struct {
	Field    string `json:"field"`
	Key      string `json:"key"`
	Internal string `json:"internal"`
	Exchange string `json:"exchange"`
}

// CompareResult holds the result of comparing one aspect of state.
type CompareResult struct {
	SnapshotType  string         `json:"snapshot_type"` // "positions", "orders", "balance"
	Matches       bool           `json:"matches"`
	Mismatches    []Mismatch     `json:"mismatches,omitempty"`
	InternalState interface{}    `json:"internal_state"`
	ExchangeState interface{}    `json:"exchange_state"`
	Severity      string         `json:"severity"` // "info", "warn", "critical"
}

// ComparePositions compares internal position state (risk.positions DB) against
// the mock exchange's position tracking.
func (c *Comparator) ComparePositions(ctx context.Context) (*CompareResult, error) {
	// Get internal positions from DB
	rows, err := c.db.Query(ctx,
		`SELECT market_id, SUM(net_quantity) as net_qty
		 FROM risk.positions
		 GROUP BY market_id
		 HAVING SUM(net_quantity) != 0`)
	if err != nil {
		return nil, fmt.Errorf("query internal positions: %w", err)
	}
	defer rows.Close()

	internal := make(map[string]int32)
	for rows.Next() {
		var marketID string
		var qty int32
		if err := rows.Scan(&marketID, &qty); err != nil {
			return nil, fmt.Errorf("scan position: %w", err)
		}
		internal[marketID] = qty
	}

	// Get exchange positions
	exchange := c.venue.GetExchange().GetPositions()

	// Compare
	mismatches := comparePositionMaps(internal, exchange)

	severity := "info"
	if len(mismatches) > 0 {
		severity = "critical" // position mismatches are always critical
	}

	return &CompareResult{
		SnapshotType:  "positions",
		Matches:       len(mismatches) == 0,
		Mismatches:    mismatches,
		InternalState: internal,
		ExchangeState: exchange,
		Severity:      severity,
	}, nil
}

// CompareOrders compares internal open orders (execution.orders DB) against
// the mock exchange's order tracking.
func (c *Comparator) CompareOrders(ctx context.Context) (*CompareResult, error) {
	// Get internal open orders from DB
	rows, err := c.db.Query(ctx,
		`SELECT internal_order_id, exchange_order_id, status, filled_quantity, quantity
		 FROM execution.orders
		 WHERE status IN ('pending', 'open', 'partially_filled')`)
	if err != nil {
		return nil, fmt.Errorf("query internal orders: %w", err)
	}
	defer rows.Close()

	type internalOrder struct {
		InternalID     string `json:"internal_order_id"`
		ExchangeID     string `json:"exchange_order_id"`
		Status         string `json:"status"`
		FilledQuantity int32  `json:"filled_quantity"`
		Quantity       int32  `json:"quantity"`
	}

	internalOrders := make(map[string]*internalOrder)
	for rows.Next() {
		var o internalOrder
		if err := rows.Scan(&o.InternalID, &o.ExchangeID, &o.Status, &o.FilledQuantity, &o.Quantity); err != nil {
			return nil, fmt.Errorf("scan order: %w", err)
		}
		internalOrders[o.ExchangeID] = &o
	}

	// Compare each internal open order against exchange state
	var mismatches []Mismatch
	exch := c.venue.GetExchange()

	for exchID, intOrder := range internalOrders {
		exchOrder, exists := exch.GetOrder(exchID)
		if !exists {
			mismatches = append(mismatches, Mismatch{
				Field:    "order_existence",
				Key:      exchID,
				Internal: intOrder.Status,
				Exchange: "not_found",
			})
			continue
		}
		// Check filled quantity
		if intOrder.FilledQuantity != exchOrder.FilledQty {
			mismatches = append(mismatches, Mismatch{
				Field:    "filled_quantity",
				Key:      exchID,
				Internal: fmt.Sprintf("%d", intOrder.FilledQuantity),
				Exchange: fmt.Sprintf("%d", exchOrder.FilledQty),
			})
		}
		// Check status alignment
		if !statusesAlign(intOrder.Status, exchOrder.Status) {
			mismatches = append(mismatches, Mismatch{
				Field:    "status",
				Key:      exchID,
				Internal: intOrder.Status,
				Exchange: exchOrder.Status,
			})
		}
	}

	severity := "info"
	if len(mismatches) > 0 {
		severity = "warn"
		// Filled qty mismatch is critical
		for _, m := range mismatches {
			if m.Field == "filled_quantity" || m.Field == "order_existence" {
				severity = "critical"
				break
			}
		}
	}

	return &CompareResult{
		SnapshotType:  "orders",
		Matches:       len(mismatches) == 0,
		Mismatches:    mismatches,
		InternalState: internalOrders,
		ExchangeState: summarizeExchangeOrders(exch),
		Severity:      severity,
	}, nil
}

// CompareBalance is a lightweight check that just records the exchange balance.
// In paper mode we don't track balance internally, so this is informational.
func (c *Comparator) CompareBalance(ctx context.Context) (*CompareResult, error) {
	balance := c.venue.GetExchange().GetBalance()

	return &CompareResult{
		SnapshotType:  "balance",
		Matches:       true, // informational only in paper mode
		InternalState: map[string]int64{"not_tracked": 0},
		ExchangeState: map[string]int64{"balance_micros": balance},
		Severity:      "info",
	}, nil
}

func comparePositionMaps(internal, exchange map[string]int32) []Mismatch {
	var mismatches []Mismatch

	// Check all internal positions against exchange
	for market, intQty := range internal {
		exchQty, exists := exchange[market]
		if !exists {
			mismatches = append(mismatches, Mismatch{
				Field:    "position",
				Key:      market,
				Internal: fmt.Sprintf("%d", intQty),
				Exchange: "0 (missing)",
			})
			continue
		}
		if intQty != exchQty {
			mismatches = append(mismatches, Mismatch{
				Field:    "position",
				Key:      market,
				Internal: fmt.Sprintf("%d", intQty),
				Exchange: fmt.Sprintf("%d", exchQty),
			})
		}
	}

	// Check for positions on exchange not tracked internally
	for market, exchQty := range exchange {
		if _, exists := internal[market]; !exists && exchQty != 0 {
			mismatches = append(mismatches, Mismatch{
				Field:    "position",
				Key:      market,
				Internal: "0 (missing)",
				Exchange: fmt.Sprintf("%d", exchQty),
			})
		}
	}

	return mismatches
}

// statusesAlign checks if internal and exchange statuses are compatible.
// The mock exchange uses slightly different status names.
func statusesAlign(internal, exchange string) bool {
	if internal == exchange {
		return true
	}
	// open in internal matches open in exchange
	// partially_filled in internal matches partially_filled in exchange
	return false
}

func summarizeExchangeOrders(exch *mockexchange.Server) map[string]string {
	// We can't iterate all exchange orders without exporting more methods,
	// so just return a marker. The internal state has the detail.
	return map[string]string{"source": "mock_exchange"}
}
