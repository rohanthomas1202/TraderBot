package execution

import (
	"context"
	"testing"
	"time"

	"autonomy-platform/internal/models"
	"autonomy-platform/services/mockexchange"
)

func TestPaperAdapter_SubmitAndFill(t *testing.T) {
	adapter := NewPaperAdapter(mockexchange.Config{
		FillDelayMs:          50,
		FillProbability:      1.0,
		PartialFillProb:      0.0,
		RejectionRate:        0.0,
		InitialBalanceMicros: 100_000_000_000,
	})

	ctx := context.Background()
	order := &models.ProposedOrder{
		TraceID:        "test-trace-1",
		StrategyID:     "test-strategy",
		Venue:          "mock",
		MarketID:       "TEST-MKT",
		Side:           models.SideBuy,
		Quantity:       5,
		PriceMicros:    400_000,
		NotionalMicros: 2_000_000,
	}

	ack, err := adapter.SubmitOrder(ctx, order, "internal-1")
	if err != nil {
		t.Fatalf("submit order: %v", err)
	}
	if ack.Status != "open" {
		t.Fatalf("expected status open, got %s", ack.Status)
	}
	if ack.ExchangeOrderID == "" {
		t.Fatal("expected non-empty exchange order ID")
	}

	// Wait for fill
	time.Sleep(200 * time.Millisecond)

	fills, err := adapter.PollFills(ctx, time.Now().Add(-1*time.Minute))
	if err != nil {
		t.Fatalf("poll fills: %v", err)
	}
	if len(fills) == 0 {
		t.Fatal("expected at least one fill")
	}

	fill := fills[0]
	if fill.ExchangeOrderID != ack.ExchangeOrderID {
		t.Errorf("fill exchange order ID mismatch: %s != %s", fill.ExchangeOrderID, ack.ExchangeOrderID)
	}
	if fill.Quantity != 5 {
		t.Errorf("expected fill quantity 5, got %d", fill.Quantity)
	}
	if fill.PriceMicros != 400_000 {
		t.Errorf("expected fill price 400000, got %d", fill.PriceMicros)
	}
}

func TestPaperAdapter_CancelOrder(t *testing.T) {
	adapter := NewPaperAdapter(mockexchange.Config{
		FillDelayMs:          999999, // no fill during test
		FillProbability:      0.0,
		InitialBalanceMicros: 100_000_000_000,
	})

	ctx := context.Background()
	order := &models.ProposedOrder{
		TraceID:        "test-trace-cancel",
		StrategyID:     "test-strategy",
		Venue:          "mock",
		MarketID:       "TEST-MKT",
		Side:           models.SideBuy,
		Quantity:       1,
		PriceMicros:    300_000,
		NotionalMicros: 300_000,
	}

	ack, err := adapter.SubmitOrder(ctx, order, "internal-cancel")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	err = adapter.CancelOrder(ctx, ack.ExchangeOrderID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	status, err := adapter.GetOrderStatus(ctx, ack.ExchangeOrderID)
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if status.Status != "cancelled" {
		t.Errorf("expected cancelled, got %s", status.Status)
	}
}

func TestPaperAdapter_CancelAll(t *testing.T) {
	adapter := NewPaperAdapter(mockexchange.Config{
		FillDelayMs:          999999,
		FillProbability:      0.0,
		InitialBalanceMicros: 100_000_000_000,
	})

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		order := &models.ProposedOrder{
			TraceID:    "cancel-all-" + string(rune('a'+i)),
			StrategyID: "test", Venue: "mock", MarketID: "TEST-MKT",
			Side: models.SideBuy, Quantity: 1,
			PriceMicros: 300_000, NotionalMicros: 300_000,
		}
		adapter.SubmitOrder(ctx, order, "internal-"+string(rune('a'+i)))
	}

	count, err := adapter.CancelAll(ctx)
	if err != nil {
		t.Fatalf("cancel all: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 cancelled, got %d", count)
	}
}

func TestPaperAdapter_Rejection(t *testing.T) {
	adapter := NewPaperAdapter(mockexchange.Config{
		FillDelayMs:          100,
		FillProbability:      1.0,
		RejectionRate:        1.0, // always reject
		InitialBalanceMicros: 100_000_000_000,
	})

	ctx := context.Background()
	order := &models.ProposedOrder{
		TraceID:        "test-reject",
		StrategyID:     "test",
		Venue:          "mock",
		MarketID:       "TEST-MKT",
		Side:           models.SideBuy,
		Quantity:       1,
		PriceMicros:    300_000,
		NotionalMicros: 300_000,
	}

	ack, err := adapter.SubmitOrder(ctx, order, "internal-reject")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if ack.Status != "rejected" {
		t.Errorf("expected rejected, got %s", ack.Status)
	}
	if ack.RejectReason == "" {
		t.Error("expected non-empty reject reason")
	}
}

func TestPaperAdapter_PartialFill(t *testing.T) {
	adapter := NewPaperAdapter(mockexchange.Config{
		FillDelayMs:          50,
		FillProbability:      1.0,
		PartialFillProb:      1.0, // always partial
		RejectionRate:        0.0,
		InitialBalanceMicros: 100_000_000_000,
	})

	ctx := context.Background()
	order := &models.ProposedOrder{
		TraceID:        "test-partial",
		StrategyID:     "test",
		Venue:          "mock",
		MarketID:       "TEST-MKT",
		Side:           models.SideBuy,
		Quantity:       10,
		PriceMicros:    300_000,
		NotionalMicros: 3_000_000,
	}

	ack, err := adapter.SubmitOrder(ctx, order, "internal-partial")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Wait for partial fills to complete
	time.Sleep(500 * time.Millisecond)

	fills, err := adapter.PollFills(ctx, time.Now().Add(-1*time.Minute))
	if err != nil {
		t.Fatalf("poll fills: %v", err)
	}

	// With partial fills enabled and quantity=10, we should get multiple fills
	totalQty := int32(0)
	for _, f := range fills {
		if f.ExchangeOrderID == ack.ExchangeOrderID {
			totalQty += f.Quantity
		}
	}
	// We expect the total to eventually reach 10 (might take multiple fill rounds)
	t.Logf("Partial fills: %d fills, total quantity %d", len(fills), totalQty)
	if totalQty == 0 {
		t.Error("expected at least some fills")
	}
}
