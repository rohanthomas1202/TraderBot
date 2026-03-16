package data

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"autonomy-platform/internal/events"
	"autonomy-platform/internal/metrics"
	"autonomy-platform/internal/models"
)

// Ingestion manages market data feeds and publishes normalized data.
type Ingestion struct {
	publisher *events.Publisher
	logger    *slog.Logger

	mu        sync.RWMutex
	latest    map[string]*models.MarketData // venue:market_id → data
}

func NewIngestion(publisher *events.Publisher) *Ingestion {
	return &Ingestion{
		publisher: publisher,
		logger:    slog.Default().With("service", "data-ingestion"),
		latest:    make(map[string]*models.MarketData),
	}
}

// RunMockFeed generates synthetic market data for paper trading.
// In production, this would be replaced by Kalshi/Polymarket websocket feeds.
func (ing *Ingestion) RunMockFeed(ctx context.Context, interval time.Duration) {
	// Seed some mock markets
	markets := []string{
		"MOCK-BTC-100K",
		"MOCK-ETH-5K",
		"MOCK-FED-RATE-CUT",
		"MOCK-RAIN-NYC-TOMORROW",
		"MOCK-SUPERBOWL-WINNER",
	}

	// Initialize with random mid prices
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	midPrices := make(map[string]int64)
	for _, m := range markets {
		midPrices[m] = int64(rng.Intn(800_000)) + 100_000 // 10¢ to 90¢
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cycleStart := time.Now()
			for _, marketID := range markets {
				// Random walk the mid price
				delta := int64(rng.Intn(20_000)) - 10_000 // ±1¢
				midPrices[marketID] += delta
				if midPrices[marketID] < 20_000 {
					midPrices[marketID] = 20_000
				}
				if midPrices[marketID] > 980_000 {
					midPrices[marketID] = 980_000
				}

				mid := midPrices[marketID]
				spread := int64(rng.Intn(30_000)) + 10_000 // 1¢ to 4¢ spread
				bid := mid - spread/2
				ask := mid + spread/2

				md := &models.MarketData{
					Venue:           "mock",
					MarketID:        marketID,
					DisplayName:     marketID,
					BidPriceMicros:  bid,
					AskPriceMicros:  ask,
					LastPriceMicros: mid,
					BidDepth:        int32(rng.Intn(10)) + 1,
					AskDepth:        int32(rng.Intn(10)) + 1,
					Volume24h:       float64(rng.Intn(100000)),
					UpdatedAt:       time.Now().UTC(),
				}

				ing.mu.Lock()
				ing.latest["mock:"+marketID] = md
				ing.mu.Unlock()

				// Publish to NATS
				ing.publisher.Publish(events.SubjectMarketData+".mock."+marketID, events.MarketDataEvent{
					Venue:           "mock",
					MarketID:        marketID,
					BidPriceMicros:  bid,
					AskPriceMicros:  ask,
					LastPriceMicros: mid,
					BidDepth:        md.BidDepth,
					AskDepth:        md.AskDepth,
					Timestamp:       md.UpdatedAt,
				})

				metrics.MarketDataAgeSec.WithLabelValues("mock", marketID).Set(0)
			}
			metrics.DataIngestionLatency.WithLabelValues("mock").Observe(time.Since(cycleStart).Seconds())
		}
	}
}

// GetLatest returns the most recent market data snapshot.
func (ing *Ingestion) GetLatest() map[string]*models.MarketData {
	ing.mu.RLock()
	defer ing.mu.RUnlock()
	// Return a copy of the map (values are pointers, but that's OK for read)
	result := make(map[string]*models.MarketData, len(ing.latest))
	for k, v := range ing.latest {
		result[k] = v
	}
	return result
}

// GetMarketData returns data for a specific market.
func (ing *Ingestion) GetMarketData(venue, marketID string) *models.MarketData {
	ing.mu.RLock()
	defer ing.mu.RUnlock()
	return ing.latest[venue+":"+marketID]
}
