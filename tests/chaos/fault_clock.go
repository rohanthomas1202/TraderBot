//go:build chaos

package chaos

import (
	"time"

	"autonomy-platform/internal/models"
	"autonomy-platform/services/risk"
)

// InjectStaleMarketData overwrites a market's data with a stale timestamp.
func InjectStaleMarketData(engine *risk.Engine, venue, marketID string, staleAge time.Duration) {
	engine.UpdateMarketData(&models.MarketData{
		Venue:           venue,
		MarketID:        marketID,
		BidPriceMicros:  350_000,
		AskPriceMicros:  360_000,
		LastPriceMicros: 355_000,
		BidDepth:        5,
		AskDepth:        5,
		UpdatedAt:       time.Now().Add(-staleAge),
	})
}

// InjectFreshMarketData overwrites a market's data with a current timestamp.
func InjectFreshMarketData(engine *risk.Engine, venue, marketID string) {
	engine.UpdateMarketData(&models.MarketData{
		Venue:           venue,
		MarketID:        marketID,
		BidPriceMicros:  350_000,
		AskPriceMicros:  360_000,
		LastPriceMicros: 355_000,
		BidDepth:        5,
		AskDepth:        5,
		UpdatedAt:       time.Now(),
	})
}
