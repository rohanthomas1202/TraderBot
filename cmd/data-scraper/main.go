package main

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"autonomy-platform/internal/health"
	"autonomy-platform/internal/logging"
	"autonomy-platform/pkg/kalshi"
	"autonomy-platform/services/backtest"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := logging.SetupLogger("data-scraper")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Connect to Postgres
	dbPool, err := pgxpool.New(ctx, envOrDefault("POSTGRES_URL", "postgres://trader:localdev@localhost:5432/autonomy?sslmode=disable"))
	if err != nil {
		logger.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	// Create Kalshi client
	keyID := os.Getenv("KALSHI_API_KEY_ID")
	keyPath := os.Getenv("KALSHI_PRIVATE_KEY_PATH")
	if keyID == "" || keyPath == "" {
		logger.Error("KALSHI_API_KEY_ID and KALSHI_PRIVATE_KEY_PATH are required")
		os.Exit(1)
	}

	client, err := kalshi.NewClient(kalshi.Config{
		BaseURL:        envOrDefault("KALSHI_BASE_URL", "https://api.elections.kalshi.com/trade-api/v2"),
		KeyID:          keyID,
		PrivateKeyPath: keyPath,
	})
	if err != nil {
		logger.Error("failed to create kalshi client", "error", err)
		os.Exit(1)
	}

	intervalSec, _ := strconv.Atoi(envOrDefault("SCRAPE_INTERVAL_SEC", "60"))
	interval := time.Duration(intervalSec) * time.Second

	// Health endpoint
	health.New("50083", "data-scraper").Start()

	scraper := backtest.NewScraper(client, dbPool, interval, logger)
	logger.Info("data-scraper starting", "interval", interval)
	if err := scraper.Run(ctx); err != nil {
		logger.Error("scraper failed", "error", err)
		os.Exit(1)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
