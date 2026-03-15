package execution

import (
	"context"
	"log/slog"
	"time"

	"autonomy-platform/internal/models"
	"autonomy-platform/services/mockexchange"
)

// PaperAdapter implements VenueAdapter using an in-process mockexchange.Server.
// This provides configurable fill delay, probability, partial fills, and rejections.
type PaperAdapter struct {
	exchange *mockexchange.Server
	logger   *slog.Logger
}

func NewPaperAdapter(cfg mockexchange.Config) *PaperAdapter {
	return &PaperAdapter{
		exchange: mockexchange.NewServer(cfg),
		logger:   slog.Default().With("component", "paper-adapter"),
	}
}

func NewPaperAdapterDefault() *PaperAdapter {
	return NewPaperAdapter(mockexchange.DefaultConfig())
}

func (p *PaperAdapter) SubmitOrder(ctx context.Context, order *models.ProposedOrder, internalID string) (*ExchangeAck, error) {
	exchOrder, err := p.exchange.SubmitOrder(ctx, internalID, order.MarketID, string(order.Side), order.Quantity, order.PriceMicros)
	if err != nil {
		return nil, err
	}

	ack := &ExchangeAck{
		ExchangeOrderID: exchOrder.ExchangeID,
		Status:          exchOrder.Status,
	}
	if exchOrder.Status == "rejected" {
		ack.RejectReason = "simulated_rejection"
	}
	return ack, nil
}

func (p *PaperAdapter) CancelOrder(ctx context.Context, exchangeOrderID string) error {
	return p.exchange.CancelOrder(exchangeOrderID)
}

func (p *PaperAdapter) CancelAll(ctx context.Context) (int, error) {
	return p.exchange.CancelAll(), nil
}

func (p *PaperAdapter) PollFills(ctx context.Context, since time.Time) ([]ExchangeFill, error) {
	mockFills := p.exchange.PollFills(since)
	fills := make([]ExchangeFill, len(mockFills))
	for i, f := range mockFills {
		fills[i] = ExchangeFill{
			FillID:          f.FillID,
			ExchangeOrderID: f.ExchangeID,
			ClientOrderID:   f.ClientID,
			Quantity:        f.Quantity,
			PriceMicros:     f.PriceMicros,
			FeeMicros:       f.FeeMicros,
			FilledAt:        f.FilledAt,
		}
	}
	return fills, nil
}

func (p *PaperAdapter) GetOrderStatus(ctx context.Context, exchangeOrderID string) (*ExchangeOrderStatus, error) {
	order, exists := p.exchange.GetOrder(exchangeOrderID)
	if !exists {
		return nil, nil
	}
	return &ExchangeOrderStatus{
		ExchangeOrderID:   exchangeOrderID,
		Status:            order.Status,
		FilledQuantity:    order.FilledQty,
		RemainingQuantity: order.Quantity - order.FilledQty,
	}, nil
}

// GetExchange returns the underlying mock exchange for inspection in tests.
func (p *PaperAdapter) GetExchange() *mockexchange.Server {
	return p.exchange
}
