package backtest

import (
	"context"
	"log/slog"
	"time"

	"autonomy-platform/pkg/kalshi"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Scraper continuously collects orderbook snapshots from Kalshi.
type Scraper struct {
	client   *kalshi.Client
	db       *pgxpool.Pool
	interval time.Duration
	logger   *slog.Logger
}

// NewScraper creates a new market data scraper.
func NewScraper(client *kalshi.Client, db *pgxpool.Pool, interval time.Duration, logger *slog.Logger) *Scraper {
	return &Scraper{
		client:   client,
		db:       db,
		interval: interval,
		logger:   logger,
	}
}

// Run starts the scraper loop. Blocks until ctx is cancelled.
func (s *Scraper) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.logger.Info("scraper started", "interval", s.interval)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.scrapeOnce(ctx)
		}
	}
}

func (s *Scraper) scrapeOnce(ctx context.Context) {
	// Get active markets
	resp, err := s.client.GetMarkets(ctx, "", 100)
	if err != nil {
		s.logger.Error("failed to fetch markets", "error", err)
		return
	}

	now := time.Now().UTC()
	inserted := 0

	for _, m := range resp.Markets {
		if m.Status != "open" {
			continue
		}

		bestBid := int64(m.YesBid) * 10_000 // cents → micros
		bestAsk := int64(m.YesAsk) * 10_000

		_, err := s.db.Exec(ctx,
			`INSERT INTO backtest.market_snapshots (venue, market_id, captured_at, best_bid_micros, best_ask_micros, volume)
			 VALUES ('kalshi', $1, $2, $3, $4, $5)`,
			m.Ticker, now, bestBid, bestAsk, int64(m.Volume))
		if err != nil {
			s.logger.Error("failed to insert snapshot", "market", m.Ticker, "error", err)
			continue
		}
		inserted++
	}

	s.logger.Info("scrape complete", "markets", inserted)
}
