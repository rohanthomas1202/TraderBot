package backtest

import (
	"testing"

	"autonomy-platform/internal/models"
)

func TestDeterministicFiller_BuyFill(t *testing.T) {
	f := &DeterministicFiller{}
	order := &models.ProposedOrder{
		Side:        models.SideBuy,
		Quantity:    10,
		PriceMicros: 500_000,
	}
	md := &models.MarketData{
		AskPriceMicros: 450_000, // ask <= order price → fill
	}

	qty, price := f.SimulateFill(order, md)
	if qty != 10 {
		t.Errorf("expected fill qty 10, got %d", qty)
	}
	if price != 450_000 {
		t.Errorf("expected fill price 450000, got %d", price)
	}
}

func TestDeterministicFiller_BuyNoFill(t *testing.T) {
	f := &DeterministicFiller{}
	order := &models.ProposedOrder{
		Side:        models.SideBuy,
		Quantity:    10,
		PriceMicros: 400_000,
	}
	md := &models.MarketData{
		AskPriceMicros: 500_000, // ask > order price → no fill
	}

	qty, _ := f.SimulateFill(order, md)
	if qty != 0 {
		t.Errorf("expected no fill, got qty %d", qty)
	}
}

func TestDeterministicFiller_SellFill(t *testing.T) {
	f := &DeterministicFiller{}
	order := &models.ProposedOrder{
		Side:        models.SideSell,
		Quantity:    5,
		PriceMicros: 600_000,
	}
	md := &models.MarketData{
		BidPriceMicros: 650_000, // bid >= order price → fill
	}

	qty, price := f.SimulateFill(order, md)
	if qty != 5 {
		t.Errorf("expected fill qty 5, got %d", qty)
	}
	if price != 650_000 {
		t.Errorf("expected fill price 650000, got %d", price)
	}
}

func TestDeterministicFiller_SellNoFill(t *testing.T) {
	f := &DeterministicFiller{}
	order := &models.ProposedOrder{
		Side:        models.SideSell,
		Quantity:    5,
		PriceMicros: 700_000,
	}
	md := &models.MarketData{
		BidPriceMicros: 650_000, // bid < order price → no fill
	}

	qty, _ := f.SimulateFill(order, md)
	if qty != 0 {
		t.Errorf("expected no fill, got qty %d", qty)
	}
}

func TestDeterministicFiller_NilMarketData(t *testing.T) {
	f := &DeterministicFiller{}
	order := &models.ProposedOrder{
		Side:        models.SideBuy,
		Quantity:    10,
		PriceMicros: 500_000,
	}

	qty, _ := f.SimulateFill(order, nil)
	if qty != 0 {
		t.Errorf("expected no fill with nil data, got qty %d", qty)
	}
}
