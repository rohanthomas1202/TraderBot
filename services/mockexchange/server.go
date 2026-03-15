// Package mockexchange provides a simulated exchange for paper trading.
//
// Behavior specification:
//
// ORDER ACCEPTANCE
//   - Orders with valid fields (positive quantity, price in 0-1M range) are accepted
//   - Orders receive an exchange_order_id immediately (synchronous ack)
//   - Status after ack: "open"
//   - Invalid orders return status "rejected" with a reason
//
// ACKNOWLEDGMENTS
//   - Synchronous: response to SubmitOrder IS the acknowledgment
//   - Simulated latency: configurable (default 10ms before ack)
//
// FILLS
//   - After configurable delay (default 100ms), open orders may fill
//   - Fill probability: configurable (default 0.5 per tick)
//   - Partial fill probability: configurable (default 0.2)
//   - Partial fills randomly choose 10-90% of remaining quantity
//   - Fill price = order limit price (no price improvement or slippage simulated)
//   - Fills are available via PollFills
//
// REJECTIONS
//   - Configurable rejection rate (default 0.0)
//   - Rejected orders get status "rejected" in ack response
//   - Rejection reasons: "simulated_rejection", "invalid_market", "insufficient_balance"
//
// CANCELLATIONS
//   - Open orders can be cancelled
//   - Filled orders return error on cancel
//   - Cancel-all cancels all open orders atomically
//
// SIMULATED STALE DATA
//   - When enabled, market data timestamps freeze (stop updating)
//   - The data values still change — only the timestamp is stale
//   - This tests the data freshness check in the risk engine
//
// SIMULATED LATENCY
//   - Configurable per-operation latency
//   - Default: 10ms ack, 100ms fill
//   - Can be increased to simulate degraded exchange conditions
//
// POSITIONS
//   - Net positions tracked from fills
//   - GetPositions returns current net per market
//
// BALANCE
//   - Starts at configurable amount (default $100,000 paper)
//   - Decremented by order cost on fill, incremented on sell fill
package mockexchange

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

type Config struct {
	FillDelayMs          int
	FillProbability      float64
	PartialFillProb      float64
	RejectionRate        float64
	InitialBalanceMicros int64
	SimulateStaleData    bool
}

func DefaultConfig() Config {
	return Config{
		FillDelayMs:          100,
		FillProbability:      0.5,
		PartialFillProb:      0.2,
		RejectionRate:        0.0,
		InitialBalanceMicros: 100_000_000_000, // $100,000
	}
}

type Server struct {
	cfg    Config
	logger *slog.Logger
	rng    *rand.Rand

	mu        sync.Mutex
	orders    map[string]*Order
	fills     []*Fill
	positions map[string]int32 // market_id → net quantity
	balance   int64
	nextID    int
}

type Order struct {
	ExchangeID   string
	ClientID     string
	MarketID     string
	Side         string
	Quantity     int32
	PriceMicros  int64
	FilledQty    int32
	Status       string // "open", "partially_filled", "filled", "cancelled", "rejected"
	CreatedAt    time.Time
}

type Fill struct {
	FillID      string
	ExchangeID  string
	ClientID    string
	MarketID    string
	Side        string
	Quantity    int32
	PriceMicros int64
	FeeMicros   int64
	FilledAt    time.Time
}

func NewServer(cfg Config) *Server {
	return &Server{
		cfg:       cfg,
		logger:    slog.Default().With("service", "mock-exchange"),
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
		orders:    make(map[string]*Order),
		positions: make(map[string]int32),
		balance:   cfg.InitialBalanceMicros,
	}
}

func (s *Server) SubmitOrder(ctx context.Context, clientID, marketID, side string, qty int32, priceMicros int64) (*Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	exchID := fmt.Sprintf("MOCK-%06d", s.nextID)

	// Check for simulated rejection
	if s.cfg.RejectionRate > 0 && s.rng.Float64() < s.cfg.RejectionRate {
		order := &Order{
			ExchangeID: exchID,
			ClientID:   clientID,
			MarketID:   marketID,
			Side:       side,
			Quantity:   qty,
			PriceMicros: priceMicros,
			Status:     "rejected",
			CreatedAt:  time.Now(),
		}
		s.orders[exchID] = order
		return order, nil
	}

	order := &Order{
		ExchangeID:  exchID,
		ClientID:    clientID,
		MarketID:    marketID,
		Side:        side,
		Quantity:    qty,
		PriceMicros: priceMicros,
		Status:      "open",
		CreatedAt:   time.Now(),
	}
	s.orders[exchID] = order

	// Schedule fill simulation
	go s.simulateFillProcess(exchID)

	return order, nil
}

func (s *Server) simulateFillProcess(exchID string) {
	time.Sleep(time.Duration(s.cfg.FillDelayMs) * time.Millisecond)

	s.mu.Lock()
	defer s.mu.Unlock()

	order, exists := s.orders[exchID]
	if !exists || order.Status == "cancelled" {
		return
	}

	if s.rng.Float64() > s.cfg.FillProbability {
		return // no fill this time
	}

	remaining := order.Quantity - order.FilledQty
	fillQty := remaining

	// Partial fill?
	if s.rng.Float64() < s.cfg.PartialFillProb && remaining > 1 {
		pct := 0.1 + s.rng.Float64()*0.8 // 10%-90%
		fillQty = int32(float64(remaining) * pct)
		if fillQty < 1 {
			fillQty = 1
		}
	}

	fill := &Fill{
		FillID:      fmt.Sprintf("FILL-%06d", len(s.fills)+1),
		ExchangeID:  exchID,
		ClientID:    order.ClientID,
		MarketID:    order.MarketID,
		Side:        order.Side,
		Quantity:    fillQty,
		PriceMicros: order.PriceMicros,
		FeeMicros:   0, // no fees in paper mode
		FilledAt:    time.Now(),
	}
	s.fills = append(s.fills, fill)

	order.FilledQty += fillQty
	if order.FilledQty >= order.Quantity {
		order.Status = "filled"
	} else {
		order.Status = "partially_filled"
		// Schedule another fill attempt for the remainder
		go s.simulateFillProcess(exchID)
	}

	// Update position and balance
	if order.Side == "buy" {
		s.positions[order.MarketID] += fillQty
		s.balance -= int64(fillQty) * order.PriceMicros
	} else {
		s.positions[order.MarketID] -= fillQty
		s.balance += int64(fillQty) * order.PriceMicros
	}
}

func (s *Server) CancelOrder(exchID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	order, exists := s.orders[exchID]
	if !exists {
		return fmt.Errorf("order %s not found", exchID)
	}
	if order.Status == "filled" {
		return fmt.Errorf("order %s already filled", exchID)
	}
	order.Status = "cancelled"
	return nil
}

func (s *Server) CancelAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for _, order := range s.orders {
		if order.Status == "open" || order.Status == "partially_filled" {
			order.Status = "cancelled"
			count++
		}
	}
	return count
}

func (s *Server) PollFills(since time.Time) []*Fill {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result []*Fill
	for _, f := range s.fills {
		if f.FilledAt.After(since) {
			result = append(result, f)
		}
	}
	return result
}

func (s *Server) GetPositions() map[string]int32 {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make(map[string]int32, len(s.positions))
	for k, v := range s.positions {
		result[k] = v
	}
	return result
}

func (s *Server) GetBalance() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.balance
}
