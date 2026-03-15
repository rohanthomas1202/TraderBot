package strategy

import (
	"context"
	"log/slog"
	"time"

	"autonomy-platform/internal/events"
	"autonomy-platform/internal/models"

	"github.com/google/uuid"
)

// SignalFunc generates trading signals from market data.
// Each strategy implements this interface.
type SignalFunc func(data map[string]*models.MarketData) []Signal

type Signal struct {
	MarketID    string
	Side        models.Side
	Quantity    int32
	PriceMicros int64
	Reason      string
}

// OrderEvaluator is the interface to the risk engine for order approval.
type OrderEvaluator interface {
	EvaluateOrder(ctx context.Context, order *models.ProposedOrder) (approved bool, err error)
}

// Engine is the strategy engine. It consumes market data, generates signals,
// and proposes orders to the risk engine.
type Engine struct {
	strategyID string
	venue      string
	signalFn   SignalFunc
	evaluator  OrderEvaluator
	publisher  *events.Publisher
	logger     *slog.Logger
}

func NewEngine(strategyID, venue string, signalFn SignalFunc, evaluator OrderEvaluator, publisher *events.Publisher) *Engine {
	return &Engine{
		strategyID: strategyID,
		venue:      venue,
		signalFn:   signalFn,
		evaluator:  evaluator,
		publisher:  publisher,
		logger:     slog.Default().With("service", "strategy-engine", "strategy", strategyID),
	}
}

// RunSignalLoop generates signals periodically and proposes orders.
func (e *Engine) RunSignalLoop(ctx context.Context, interval time.Duration, dataSource func() map[string]*models.MarketData) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			data := dataSource()
			if len(data) == 0 {
				continue
			}
			e.processSignals(ctx, data)
		}
	}
}

func (e *Engine) processSignals(ctx context.Context, data map[string]*models.MarketData) {
	signals := e.signalFn(data)

	for _, sig := range signals {
		order := &models.ProposedOrder{
			TraceID:        uuid.New().String(),
			StrategyID:     e.strategyID,
			Venue:          e.venue,
			MarketID:       sig.MarketID,
			Side:           sig.Side,
			Quantity:       sig.Quantity,
			PriceMicros:    sig.PriceMicros,
			NotionalMicros: int64(sig.Quantity) * sig.PriceMicros,
			ProposedAt:     time.Now().UTC(),
		}

		if err := order.Validate(); err != nil {
			e.logger.Error("invalid signal produced", "error", err, "market", sig.MarketID)
			continue
		}

		// Publish proposal event
		e.publisher.Publish(events.SubjectOrderProposed+"."+e.strategyID, events.OrderProposedEvent{
			TraceID:        order.TraceID,
			StrategyID:     e.strategyID,
			Venue:          e.venue,
			MarketID:       sig.MarketID,
			Side:           string(sig.Side),
			Quantity:       sig.Quantity,
			PriceMicros:    sig.PriceMicros,
			NotionalMicros: order.NotionalMicros,
			Timestamp:      order.ProposedAt,
		})

		// Send to risk engine for evaluation
		approved, err := e.evaluator.EvaluateOrder(ctx, order)
		if err != nil {
			e.logger.Error("risk evaluation failed", "trace_id", order.TraceID, "error", err)
			continue
		}

		if approved {
			e.logger.Info("order approved",
				"trace_id", order.TraceID,
				"market", sig.MarketID,
				"side", sig.Side,
				"qty", sig.Quantity,
				"price", sig.PriceMicros,
			)
		} else {
			e.logger.Debug("order denied",
				"trace_id", order.TraceID,
				"market", sig.MarketID,
				"reason", sig.Reason,
			)
		}
	}
}
