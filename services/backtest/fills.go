package backtest

import (
	"autonomy-platform/internal/models"
)

// FillSimulator determines whether and how an order gets filled.
type FillSimulator interface {
	// SimulateFill returns fill quantity (0 = no fill) and fill price.
	SimulateFill(order *models.ProposedOrder, md *models.MarketData) (filledQty int32, fillPrice int64)
}

// DeterministicFiller fills buy orders if ask <= order price,
// sell orders if bid >= order price. Always fills full quantity.
type DeterministicFiller struct{}

func (d *DeterministicFiller) SimulateFill(order *models.ProposedOrder, md *models.MarketData) (int32, int64) {
	if md == nil {
		return 0, 0
	}
	switch order.Side {
	case models.SideBuy:
		if md.AskPriceMicros > 0 && md.AskPriceMicros <= order.PriceMicros {
			return order.Quantity, md.AskPriceMicros
		}
	case models.SideSell:
		if md.BidPriceMicros > 0 && md.BidPriceMicros >= order.PriceMicros {
			return order.Quantity, md.BidPriceMicros
		}
	}
	return 0, 0
}
