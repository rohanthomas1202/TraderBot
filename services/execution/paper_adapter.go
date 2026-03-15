package execution

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"autonomy-platform/internal/models"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// MockAdapter implements VenueAdapter by talking to the mock-exchange gRPC service.
// This is the Phase 1 paper-trading adapter.
type MockAdapter struct {
	addr   string
	logger *slog.Logger
	// In a full implementation, this would hold a gRPC client.
	// For Phase 1 skeleton, we use a simple in-process mock.
	mock *InProcessMock
}

// InProcessMock simulates a venue without requiring a separate gRPC service.
// This is the simplest Phase 1 path. Replace with gRPC client when mock-exchange
// service is built.
type InProcessMock struct {
	orders     map[string]*mockOrder
	fills      []ExchangeFill
	fillDelay  time.Duration
	fillProb   float64
	rejectRate float64
	nextID     int
}

type mockOrder struct {
	exchangeID   string
	clientID     string
	marketID     string
	side         string
	quantity     int32
	priceMicros  int64
	filledQty    int32
	status       string
	createdAt    time.Time
}

func NewMockAdapter(addr string) *MockAdapter {
	return &MockAdapter{
		addr:   addr,
		logger: slog.Default().With("component", "mock-adapter"),
		mock: &InProcessMock{
			orders:     make(map[string]*mockOrder),
			fillDelay:  100 * time.Millisecond,
			fillProb:   0.5,
			rejectRate: 0.0,
		},
	}
}

func (m *MockAdapter) SubmitOrder(ctx context.Context, order *models.ProposedOrder, internalID string) (*ExchangeAck, error) {
	m.mock.nextID++
	exchID := fmt.Sprintf("mock-order-%d", m.mock.nextID)

	// Simulate rejection based on configured rate
	// (deterministic for now — use random in production mock)
	if m.mock.rejectRate > 0 && m.mock.nextID%int(1/m.mock.rejectRate) == 0 {
		return &ExchangeAck{
			ExchangeOrderID: exchID,
			Status:          "rejected",
			RejectReason:    "simulated rejection",
		}, nil
	}

	m.mock.orders[exchID] = &mockOrder{
		exchangeID:  exchID,
		clientID:    internalID,
		marketID:    order.MarketID,
		side:        string(order.Side),
		quantity:    order.Quantity,
		priceMicros: order.PriceMicros,
		status:      "open",
		createdAt:   time.Now(),
	}

	// Schedule a simulated fill after delay
	go func() {
		time.Sleep(m.mock.fillDelay)
		m.simulateFill(exchID)
	}()

	return &ExchangeAck{
		ExchangeOrderID: exchID,
		Status:          "open",
	}, nil
}

func (m *MockAdapter) simulateFill(exchangeOrderID string) {
	mo, exists := m.mock.orders[exchangeOrderID]
	if !exists || mo.status != "open" {
		return
	}

	// Full fill at the limit price
	fillQty := mo.quantity - mo.filledQty
	mo.filledQty += fillQty
	mo.status = "filled"

	m.mock.fills = append(m.mock.fills, ExchangeFill{
		FillID:          fmt.Sprintf("mock-fill-%d", len(m.mock.fills)+1),
		ExchangeOrderID: exchangeOrderID,
		ClientOrderID:   mo.clientID,
		Quantity:        fillQty,
		PriceMicros:     mo.priceMicros,
		FeeMicros:       0,
		FilledAt:        time.Now(),
	})
}

func (m *MockAdapter) CancelOrder(ctx context.Context, exchangeOrderID string) error {
	mo, exists := m.mock.orders[exchangeOrderID]
	if !exists {
		return fmt.Errorf("order %s not found", exchangeOrderID)
	}
	if mo.status == "filled" {
		return fmt.Errorf("order %s already filled", exchangeOrderID)
	}
	mo.status = "cancelled"
	return nil
}

func (m *MockAdapter) CancelAll(ctx context.Context) (int, error) {
	count := 0
	for _, mo := range m.mock.orders {
		if mo.status == "open" {
			mo.status = "cancelled"
			count++
		}
	}
	return count, nil
}

func (m *MockAdapter) PollFills(ctx context.Context, since time.Time) ([]ExchangeFill, error) {
	var result []ExchangeFill
	for _, f := range m.mock.fills {
		if f.FilledAt.After(since) {
			result = append(result, f)
		}
	}
	return result, nil
}

func (m *MockAdapter) GetOrderStatus(ctx context.Context, exchangeOrderID string) (*ExchangeOrderStatus, error) {
	mo, exists := m.mock.orders[exchangeOrderID]
	if !exists {
		return nil, fmt.Errorf("order %s not found", exchangeOrderID)
	}
	return &ExchangeOrderStatus{
		ExchangeOrderID:   exchangeOrderID,
		Status:            mo.status,
		FilledQuantity:    mo.filledQty,
		RemainingQuantity: mo.quantity - mo.filledQty,
	}, nil
}

// dialMockExchange creates a gRPC connection to the mock exchange.
// Used when running mock-exchange as a separate service.
func dialMockExchange(addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}
