package mockexchange

import (
	"context"
	"testing"
	"time"
)

func TestServer_SubmitAndFill(t *testing.T) {
	srv := NewServer(Config{
		FillDelayMs:          50,
		FillProbability:      1.0,
		PartialFillProb:      0.0,
		RejectionRate:        0.0,
		InitialBalanceMicros: 100_000_000_000,
	})

	ctx := context.Background()
	order, err := srv.SubmitOrder(ctx, "client-1", "MKT-1", "buy", 5, 400_000)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if order.Status != "open" {
		t.Fatalf("expected open, got %s", order.Status)
	}

	time.Sleep(200 * time.Millisecond)

	fills := srv.PollFills(time.Now().Add(-1 * time.Minute))
	if len(fills) == 0 {
		t.Fatal("expected at least one fill")
	}

	if fills[0].Quantity != 5 {
		t.Errorf("expected fill qty 5, got %d", fills[0].Quantity)
	}
}

func TestServer_Rejection(t *testing.T) {
	srv := NewServer(Config{
		FillDelayMs:          100,
		FillProbability:      1.0,
		RejectionRate:        1.0,
		InitialBalanceMicros: 100_000_000_000,
	})

	ctx := context.Background()
	order, err := srv.SubmitOrder(ctx, "client-1", "MKT-1", "buy", 5, 400_000)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if order.Status != "rejected" {
		t.Fatalf("expected rejected, got %s", order.Status)
	}
}

func TestServer_Cancel(t *testing.T) {
	srv := NewServer(Config{
		FillDelayMs:          999999,
		FillProbability:      0.0,
		InitialBalanceMicros: 100_000_000_000,
	})

	ctx := context.Background()
	order, _ := srv.SubmitOrder(ctx, "client-1", "MKT-1", "buy", 5, 400_000)

	err := srv.CancelOrder(order.ExchangeID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	o, exists := srv.GetOrder(order.ExchangeID)
	if !exists {
		t.Fatal("order should still exist")
	}
	if o.Status != "cancelled" {
		t.Errorf("expected cancelled, got %s", o.Status)
	}
}

func TestServer_CancelAll(t *testing.T) {
	srv := NewServer(Config{
		FillDelayMs:          999999,
		FillProbability:      0.0,
		InitialBalanceMicros: 100_000_000_000,
	})

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		srv.SubmitOrder(ctx, "client", "MKT-1", "buy", 1, 300_000)
	}

	count := srv.CancelAll()
	if count != 3 {
		t.Errorf("expected 3 cancelled, got %d", count)
	}
}

func TestServer_PositionTracking(t *testing.T) {
	srv := NewServer(Config{
		FillDelayMs:          50,
		FillProbability:      1.0,
		PartialFillProb:      0.0,
		InitialBalanceMicros: 100_000_000_000,
	})

	ctx := context.Background()
	srv.SubmitOrder(ctx, "client-1", "MKT-A", "buy", 10, 500_000)
	time.Sleep(200 * time.Millisecond)

	positions := srv.GetPositions()
	if positions["MKT-A"] != 10 {
		t.Errorf("expected position 10 for MKT-A, got %d", positions["MKT-A"])
	}

	// Balance should decrease
	balance := srv.GetBalance()
	expected := int64(100_000_000_000) - 10*500_000
	if balance != expected {
		t.Errorf("expected balance %d, got %d", expected, balance)
	}
}

func TestServer_CancelFilledOrder_Fails(t *testing.T) {
	srv := NewServer(Config{
		FillDelayMs:          10,
		FillProbability:      1.0,
		PartialFillProb:      0.0,
		InitialBalanceMicros: 100_000_000_000,
	})

	ctx := context.Background()
	order, _ := srv.SubmitOrder(ctx, "client-1", "MKT-1", "buy", 1, 300_000)
	time.Sleep(100 * time.Millisecond)

	err := srv.CancelOrder(order.ExchangeID)
	if err == nil {
		t.Error("expected error when cancelling filled order")
	}
}
