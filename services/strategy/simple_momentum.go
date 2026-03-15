package strategy

import (
	"autonomy-platform/internal/models"
)

// SimpleMomentum is a Phase 1 test strategy.
// It generates buy signals when price is below a threshold and sell signals above.
// This is deliberately simple — the goal is to exercise the full pipeline,
// not to make money.
func SimpleMomentum() SignalFunc {
	return func(data map[string]*models.MarketData) []Signal {
		var signals []Signal

		for _, md := range data {
			mid := md.MidPriceMicros()
			if mid == 0 {
				continue
			}

			// Simple threshold strategy:
			// Buy if mid < 400000 (40 cents), Sell if mid > 600000 (60 cents)
			// This generates trades on markets that are away from 50/50
			if mid < 400_000 {
				signals = append(signals, Signal{
					MarketID:    md.MarketID,
					Side:        models.SideBuy,
					Quantity:    1,
					PriceMicros: mid, // bid at mid
					Reason:      "momentum: price below 0.40",
				})
			} else if mid > 600_000 {
				signals = append(signals, Signal{
					MarketID:    md.MarketID,
					Side:        models.SideSell,
					Quantity:    1,
					PriceMicros: mid, // offer at mid
					Reason:      "momentum: price above 0.60",
				})
			}
		}

		return signals
	}
}
