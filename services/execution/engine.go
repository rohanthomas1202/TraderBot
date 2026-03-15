package execution

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/events"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/risk"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VenueAdapter abstracts venue-specific order management.
// Paper mode uses the MockAdapter. Production uses KalshiAdapter or PolymarketAdapter.
type VenueAdapter interface {
	SubmitOrder(ctx context.Context, order *models.ProposedOrder, internalID string) (*ExchangeAck, error)
	CancelOrder(ctx context.Context, exchangeOrderID string) error
	CancelAll(ctx context.Context) (int, error)
	PollFills(ctx context.Context, since time.Time) ([]ExchangeFill, error)
	GetOrderStatus(ctx context.Context, exchangeOrderID string) (*ExchangeOrderStatus, error)
}

type ExchangeAck struct {
	ExchangeOrderID string
	Status          string // "open" or "rejected"
	RejectReason    string
}

type ExchangeFill struct {
	FillID          string
	ExchangeOrderID string
	ClientOrderID   string
	Quantity        int32
	PriceMicros     int64
	FeeMicros       int64
	FilledAt        time.Time
}

type ExchangeOrderStatus struct {
	ExchangeOrderID   string
	Status            string
	FilledQuantity    int32
	RemainingQuantity int32
}

// Engine manages order lifecycle: receive approved orders, submit to venue,
// track state, poll fills, update risk engine.
type Engine struct {
	db        *pgxpool.Pool
	venue     VenueAdapter
	publisher *events.Publisher
	auditor   *audit.Logger
	hmacKey   []byte
	logger    *slog.Logger

	mu          sync.RWMutex
	openOrders  map[string]*models.OrderRecord // internal_order_id → record
	systemMode  string
}

func NewEngine(db *pgxpool.Pool, venue VenueAdapter, publisher *events.Publisher, auditor *audit.Logger, hmacKey []byte) *Engine {
	return &Engine{
		db:         db,
		venue:      venue,
		publisher:  publisher,
		auditor:    auditor,
		hmacKey:    hmacKey,
		logger:     slog.Default().With("service", "execution-engine"),
		openOrders: make(map[string]*models.OrderRecord),
		systemMode: "normal",
	}
}

// LoadOpenOrders rebuilds in-memory state from DB on startup.
func (e *Engine) LoadOpenOrders(ctx context.Context) error {
	rows, err := e.db.Query(ctx,
		`SELECT internal_order_id, trace_id, strategy_id, venue, market_id, side,
		        quantity, price_micros, notional_micros, exchange_order_id, status,
		        filled_quantity, credential_id, approval_hmac, policy_config_hash,
		        proposed_at, approved_at
		 FROM execution.orders
		 WHERE status IN ('pending', 'open', 'partially_filled')`)
	if err != nil {
		return fmt.Errorf("load open orders: %w", err)
	}
	defer rows.Close()

	e.mu.Lock()
	defer e.mu.Unlock()

	count := 0
	for rows.Next() {
		var rec models.OrderRecord
		var sideStr string
		if err := rows.Scan(
			&rec.InternalOrderID, &rec.TraceID, &rec.StrategyID,
			&rec.Venue, &rec.MarketID, &sideStr,
			&rec.Quantity, &rec.PriceMicros, &rec.NotionalMicros,
			&rec.ExchangeOrderID, &rec.Status,
			&rec.FilledQuantity, &rec.CredentialID,
			&rec.ApprovalHMAC, &rec.PolicyConfigHash,
			&rec.ProposedAt, &rec.ApprovedAt,
		); err != nil {
			return fmt.Errorf("scan order: %w", err)
		}
		rec.Side = models.Side(sideStr)
		e.openOrders[rec.InternalOrderID.String()] = &rec
		count++
	}

	e.logger.Info("loaded open orders", "count", count)
	return nil
}

// SubmitOrder validates the approval, persists the order, and sends to the venue.
func (e *Engine) SubmitOrder(ctx context.Context, approval *risk.Approval) (*models.OrderRecord, error) {
	// Step 1: Verify HMAC — execution engine cannot forge approvals
	if !risk.VerifyApproval(approval, e.hmacKey) {
		return nil, fmt.Errorf("approval HMAC verification failed")
	}

	// Step 2: Verify decision is approved
	if approval.Decision != risk.DecisionApproved {
		return nil, fmt.Errorf("order not approved: %s", approval.Decision)
	}

	// Step 3: Check system mode
	e.mu.RLock()
	mode := e.systemMode
	e.mu.RUnlock()
	if mode != "normal" {
		return nil, fmt.Errorf("system in %s mode, rejecting new orders", mode)
	}

	order := approval.Order
	internalID := uuid.New()
	now := time.Now().UTC()

	// Step 4: Build idempotency key and persist order
	idempKey := fmt.Sprintf("%s:%s", order.TraceID, order.IdempotencyKey())

	rec := &models.OrderRecord{
		InternalOrderID: internalID,
		TraceID:         order.TraceID,
		StrategyID:      order.StrategyID,
		Venue:           order.Venue,
		MarketID:        order.MarketID,
		Side:            order.Side,
		Quantity:         order.Quantity,
		PriceMicros:     order.PriceMicros,
		NotionalMicros:  order.NotionalMicros,
		Status:          models.StatusPending,
		CredentialID:    "paper-mode",
		ApprovalHMAC:    approval.HMACSignature,
		PolicyConfigHash: approval.PolicyConfigHash,
		ProposedAt:      order.ProposedAt,
		ApprovedAt:      approval.DecidedAt,
		CreatedAt:       now,
	}

	_, err := e.db.Exec(ctx,
		`INSERT INTO execution.orders
		 (internal_order_id, trace_id, strategy_id, venue, market_id, side,
		  quantity, price_micros, notional_micros, status, credential_id,
		  approval_hmac, policy_config_hash, proposed_at, approved_at, idempotency_key)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		internalID, order.TraceID, order.StrategyID, order.Venue, order.MarketID, string(order.Side),
		order.Quantity, order.PriceMicros, order.NotionalMicros, string(models.StatusPending),
		"paper-mode", approval.HMACSignature, approval.PolicyConfigHash,
		order.ProposedAt, approval.DecidedAt, idempKey,
	)
	if err != nil {
		// Check if it's a duplicate (unique constraint on idempotency_key)
		return nil, fmt.Errorf("persist order: %w", err)
	}

	// Step 5: Submit to venue
	ack, err := e.venue.SubmitOrder(ctx, order, internalID.String())
	if err != nil {
		e.transitionOrder(ctx, rec, models.StatusFailed)
		return rec, fmt.Errorf("venue submit: %w", err)
	}

	rec.ExchangeOrderID = ack.ExchangeOrderID
	submittedAt := time.Now().UTC()
	rec.SubmittedAt = &submittedAt

	if ack.Status == "rejected" {
		e.transitionOrder(ctx, rec, models.StatusRejected)
		e.auditor.LogWarn(ctx, "order.rejected", order.TraceID, map[string]interface{}{
			"reason": ack.RejectReason,
		})
		return rec, nil
	}

	// Order is now open on the exchange
	e.transitionOrder(ctx, rec, models.StatusOpen)
	e.mu.Lock()
	e.openOrders[internalID.String()] = rec
	e.mu.Unlock()

	e.publisher.Publish(events.SubjectOrderSubmitted+"."+order.Venue, events.OrderSubmittedEvent{
		TraceID:         order.TraceID,
		InternalOrderID: internalID.String(),
		ExchangeOrderID: ack.ExchangeOrderID,
		CredentialID:    "paper-mode",
		Venue:           order.Venue,
		Timestamp:       submittedAt,
	})

	e.auditor.LogInfo(ctx, "order.submitted", order.TraceID, map[string]interface{}{
		"internal_order_id": internalID.String(),
		"exchange_order_id": ack.ExchangeOrderID,
		"venue":             order.Venue,
		"market_id":         order.MarketID,
	})

	return rec, nil
}

// CancelOrder cancels a specific order.
func (e *Engine) CancelOrder(ctx context.Context, internalOrderID, reason, cancelledBy string) error {
	e.mu.RLock()
	rec, exists := e.openOrders[internalOrderID]
	e.mu.RUnlock()

	if !exists {
		return fmt.Errorf("order %s not found in open orders", internalOrderID)
	}

	if err := e.venue.CancelOrder(ctx, rec.ExchangeOrderID); err != nil {
		return fmt.Errorf("venue cancel: %w", err)
	}

	e.transitionOrder(ctx, rec, models.StatusCancelled)
	e.mu.Lock()
	delete(e.openOrders, internalOrderID)
	e.mu.Unlock()

	e.publisher.Publish(events.SubjectOrderCancelled+"."+rec.Venue, events.OrderCancelledEvent{
		TraceID:         rec.TraceID,
		InternalOrderID: internalOrderID,
		Reason:          reason,
		CancelledBy:     cancelledBy,
		Timestamp:       time.Now().UTC(),
	})

	return nil
}

// CancelAll cancels all open orders. Used by kill switch.
func (e *Engine) CancelAll(ctx context.Context, reason, cancelledBy string) (int, error) {
	cancelled, err := e.venue.CancelAll(ctx)
	if err != nil {
		e.logger.Error("venue cancel-all failed", "error", err)
	}

	e.mu.Lock()
	for id, rec := range e.openOrders {
		e.transitionOrder(ctx, rec, models.StatusCancelled)
		delete(e.openOrders, id)
	}
	e.mu.Unlock()

	e.auditor.LogCritical(ctx, "order.cancel_all", "", map[string]interface{}{
		"reason":      reason,
		"cancelled_by": cancelledBy,
		"count":       cancelled,
	})

	return cancelled, err
}

// PollFills checks the venue for new fills and updates internal state.
// Called periodically by the main loop.
func (e *Engine) PollFills(ctx context.Context, since time.Time, riskCallback func(context.Context, *risk.FillReport) error) error {
	fills, err := e.venue.PollFills(ctx, since)
	if err != nil {
		return fmt.Errorf("poll fills: %w", err)
	}

	for _, f := range fills {
		if err := e.processFill(ctx, &f, riskCallback); err != nil {
			e.logger.Error("process fill failed", "fill_id", f.FillID, "error", err)
		}
	}
	return nil
}

func (e *Engine) processFill(ctx context.Context, f *ExchangeFill, riskCallback func(context.Context, *risk.FillReport) error) error {
	// Find the internal order
	e.mu.RLock()
	var rec *models.OrderRecord
	for _, r := range e.openOrders {
		if r.ExchangeOrderID == f.ExchangeOrderID {
			rec = r
			break
		}
	}
	e.mu.RUnlock()

	if rec == nil {
		e.logger.Warn("fill for unknown order", "exchange_order_id", f.ExchangeOrderID)
		return nil
	}

	// Persist fill
	fillID := uuid.New()
	_, err := e.db.Exec(ctx,
		`INSERT INTO execution.fills (fill_id, internal_order_id, exchange_fill_id, quantity, price_micros, fee_micros, filled_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		fillID, rec.InternalOrderID, f.FillID, f.Quantity, f.PriceMicros, f.FeeMicros, f.FilledAt,
	)
	if err != nil {
		return fmt.Errorf("persist fill: %w", err)
	}

	// Update order state
	rec.FilledQuantity += f.Quantity
	rec.AvgFillPriceMicros = f.PriceMicros // simplified; should be weighted avg
	now := time.Now().UTC()
	rec.LastFillAt = &now

	if rec.FilledQuantity >= rec.Quantity {
		e.transitionOrder(ctx, rec, models.StatusFilled)
		e.mu.Lock()
		delete(e.openOrders, rec.InternalOrderID.String())
		e.mu.Unlock()
	} else {
		e.transitionOrder(ctx, rec, models.StatusPartiallyFilled)
	}

	// Notify risk engine
	if riskCallback != nil {
		riskCallback(ctx, &risk.FillReport{
			TraceID:     rec.TraceID,
			StrategyID:  rec.StrategyID,
			Venue:       rec.Venue,
			MarketID:    rec.MarketID,
			Side:        rec.Side,
			Quantity:    f.Quantity,
			PriceMicros: f.PriceMicros,
		})
	}

	// Publish event
	e.publisher.Publish(events.SubjectOrderFilled+"."+rec.Venue, events.OrderFilledEvent{
		TraceID:            rec.TraceID,
		InternalOrderID:    rec.InternalOrderID.String(),
		ExchangeOrderID:    f.ExchangeOrderID,
		FilledQuantity:     f.Quantity,
		AveragePriceMicros: f.PriceMicros,
		RemainingQuantity:  rec.Quantity - rec.FilledQuantity,
		Timestamp:          f.FilledAt,
	})

	return nil
}

// SetSystemMode is called by watchdog to change the operating mode.
func (e *Engine) SetSystemMode(mode string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	old := e.systemMode
	e.systemMode = mode
	e.logger.Warn("system mode changed", "old", old, "new", mode)
}

func (e *Engine) transitionOrder(ctx context.Context, rec *models.OrderRecord, newStatus models.OrderStatus) {
	if err := models.ValidateTransition(rec.Status, newStatus); err != nil {
		e.logger.Error("illegal order transition",
			"order_id", rec.InternalOrderID,
			"from", rec.Status,
			"to", newStatus,
			"error", err,
		)
		return
	}

	now := time.Now().UTC()
	rec.Status = newStatus
	if newStatus.IsTerminal() {
		rec.CompletedAt = &now
	}

	_, err := e.db.Exec(ctx,
		`UPDATE execution.orders SET status=$1, filled_quantity=$2, avg_fill_price_micros=$3,
		 exchange_order_id=$4, submitted_at=$5, last_fill_at=$6, completed_at=$7, acknowledged_at=$8
		 WHERE internal_order_id=$9`,
		string(newStatus), rec.FilledQuantity, rec.AvgFillPriceMicros,
		rec.ExchangeOrderID, rec.SubmittedAt, rec.LastFillAt, rec.CompletedAt, rec.AcknowledgedAt,
		rec.InternalOrderID,
	)
	if err != nil {
		e.logger.Error("failed to update order status", "order_id", rec.InternalOrderID, "error", err)
	}
}

// OpenOrderCount returns the number of currently open orders.
func (e *Engine) OpenOrderCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.openOrders)
}
