package data

import (
	"context"
	"fmt"
	"os"
	"time"

	"autonomy-platform/internal/events"
	"autonomy-platform/internal/models"
	"autonomy-platform/pkg/kalshi"

	"gopkg.in/yaml.v3"
)

// MarketMapping maps a Kalshi ticker to an internal market ID.
type MarketMapping struct {
	KalshiTicker string `yaml:"kalshi_ticker"`
	InternalID   string `yaml:"internal_id"`
	DisplayName  string `yaml:"display_name"`
	Enabled      bool   `yaml:"enabled"`
}

// KalshiFeedConfig is the configuration for the Kalshi data feed.
type KalshiFeedConfig struct {
	Markets        []MarketMapping `yaml:"markets"`
	PollIntervalMs int             `yaml:"poll_interval_ms"`
	APIBaseURL     string          `yaml:"api_base_url"`
}

// LoadKalshiFeedConfig loads and parses the Kalshi market mapping YAML.
func LoadKalshiFeedConfig(path string) (*KalshiFeedConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg KalshiFeedConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.PollIntervalMs <= 0 {
		cfg.PollIntervalMs = 1000
	}
	return &cfg, nil
}

// EnabledMarkets returns only the markets with enabled: true.
func (cfg *KalshiFeedConfig) EnabledMarkets() []MarketMapping {
	var enabled []MarketMapping
	for _, m := range cfg.Markets {
		if m.Enabled {
			enabled = append(enabled, m)
		}
	}
	return enabled
}

// RunKalshiFeed polls the Kalshi API for real market data and publishes
// it to NATS using the same MarketDataEvent format as the mock feed.
// Orders still go to the mock exchange — this is shadow mode.
func (ing *Ingestion) RunKalshiFeed(ctx context.Context, client *kalshi.Client, configPath string) error {
	cfg, err := LoadKalshiFeedConfig(configPath)
	if err != nil {
		return fmt.Errorf("load kalshi feed config: %w", err)
	}

	markets := cfg.EnabledMarkets()
	if len(markets) == 0 {
		return fmt.Errorf("no enabled markets in %s", configPath)
	}

	ing.logger.Info("kalshi feed starting",
		"markets", len(markets),
		"poll_interval_ms", cfg.PollIntervalMs,
	)
	for _, m := range markets {
		ing.logger.Info("tracking market",
			"ticker", m.KalshiTicker,
			"internal_id", m.InternalID,
			"display_name", m.DisplayName,
		)
	}

	ticker := time.NewTicker(time.Duration(cfg.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			ing.logger.Info("kalshi feed shutting down")
			return nil
		case <-ticker.C:
			ing.pollAllMarkets(ctx, client, markets)
		}
	}
}

// pollAllMarkets fetches orderbook data for each market and publishes events.
func (ing *Ingestion) pollAllMarkets(ctx context.Context, client *kalshi.Client, markets []MarketMapping) {
	for _, m := range markets {
		if ctx.Err() != nil {
			return
		}
		ing.pollMarket(ctx, client, m)
	}
}

// pollMarket fetches orderbook data for a single market, converts prices,
// and publishes a MarketDataEvent.
func (ing *Ingestion) pollMarket(ctx context.Context, client *kalshi.Client, m MarketMapping) {
	ob, err := client.GetOrderbook(ctx, m.KalshiTicker)
	if err != nil {
		ing.logger.Warn("kalshi orderbook fetch failed",
			"ticker", m.KalshiTicker,
			"error", err,
		)
		return
	}

	// Extract best bid/ask from the "Yes" side of the orderbook.
	// Kalshi binary markets: Yes side has bids (buyers) and the
	// complementary No side implies asks. For simplicity, we use
	// the Yes orderbook: best bid = highest Yes bid, best ask = lowest Yes ask.
	bidCents, bidDepth := kalshi.BestBid(ob.Yes)
	askCents, askDepth := kalshi.BestAsk(ob.No)

	// Skip markets with empty orderbooks
	if bidCents == 0 && askCents == 0 {
		ing.logger.Warn("kalshi orderbook empty, skipping",
			"ticker", m.KalshiTicker,
			"internal_id", m.InternalID,
		)
		return
	}

	// If only one side exists, use it for both (with a small spread)
	if bidCents == 0 {
		bidCents = askCents - 1
		if bidCents < 1 {
			bidCents = 1
		}
	}
	if askCents == 0 {
		askCents = bidCents + 1
		if askCents > 99 {
			askCents = 99
		}
	}

	bidMicros := kalshi.CentsToMicros(bidCents)
	askMicros := kalshi.CentsToMicros(askCents)
	midMicros := (bidMicros + askMicros) / 2
	now := time.Now().UTC()

	md := &models.MarketData{
		Venue:           "kalshi",
		MarketID:        m.InternalID,
		DisplayName:     m.DisplayName,
		BidPriceMicros:  bidMicros,
		AskPriceMicros:  askMicros,
		LastPriceMicros: midMicros,
		BidDepth:        bidDepth,
		AskDepth:        askDepth,
		UpdatedAt:       now,
	}

	ing.mu.Lock()
	ing.latest["kalshi:"+m.InternalID] = md
	ing.mu.Unlock()

	ing.publisher.Publish(events.SubjectMarketData+".kalshi."+m.InternalID, events.MarketDataEvent{
		Venue:           "kalshi",
		MarketID:        m.InternalID,
		BidPriceMicros:  bidMicros,
		AskPriceMicros:  askMicros,
		LastPriceMicros: midMicros,
		BidDepth:        bidDepth,
		AskDepth:        askDepth,
		Timestamp:       now,
	})
}
