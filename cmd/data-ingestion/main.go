package main

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"autonomy-platform/internal/events"
	"autonomy-platform/internal/health"
	"autonomy-platform/internal/logging"
	"autonomy-platform/pkg/kalshi"
	"autonomy-platform/services/data"

	"github.com/nats-io/nats.go"
)

func main() {
	logger := logging.SetupLogger("data-ingestion")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Connect to NATS
	nc, err := nats.Connect(envOrDefault("NATS_URL", "nats://localhost:4222"))
	if err != nil {
		logger.Error("failed to connect to nats", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	publisher, err := events.NewPublisher(nc)
	if err != nil {
		logger.Error("failed to create event publisher", "error", err)
		os.Exit(1)
	}

	// Start health endpoint
	healthPort := envOrDefault("HEALTH_PORT", "50011")
	health.New(healthPort, "data-ingestion").Start()

	ingestion := data.NewIngestion(publisher)

	intervalMs := envOrDefaultInt("PUBLISH_INTERVAL_MS", 1000)
	logger.Info("data ingestion ready",
		"source", envOrDefault("DATA_SOURCE", "mock"),
		"interval_ms", intervalMs,
	)

	// Select data source
	dataSource := envOrDefault("DATA_SOURCE", "mock")
	switch dataSource {
	case "mock":
		go ingestion.RunMockFeed(ctx, time.Duration(intervalMs)*time.Millisecond)
	case "kalshi":
		keyID := os.Getenv("KALSHI_API_KEY_ID")
		keyPath := os.Getenv("KALSHI_PRIVATE_KEY_PATH")
		if keyID == "" || keyPath == "" {
			logger.Error("KALSHI_API_KEY_ID and KALSHI_PRIVATE_KEY_PATH required for kalshi data source")
			os.Exit(1)
		}
		kalshiCfg := kalshi.Config{
			BaseURL:        envOrDefault("KALSHI_API_BASE_URL", "https://api.elections.kalshi.com/trade-api/v2"),
			KeyID:          keyID,
			PrivateKeyPath: keyPath,
		}
		client, err := kalshi.NewClient(kalshiCfg)
		if err != nil {
			logger.Error("failed to create kalshi client", "error", err)
			os.Exit(1)
		}
		marketConfigPath := envOrDefault("KALSHI_MARKETS_CONFIG", "./configs/kalshi_markets.yaml")
		go func() {
			if err := ingestion.RunKalshiFeed(ctx, client, marketConfigPath); err != nil {
				logger.Error("kalshi feed failed", "error", err)
			}
		}()
	default:
		logger.Error("unknown DATA_SOURCE, must be 'mock' or 'kalshi'", "source", dataSource)
		os.Exit(1)
	}

	<-ctx.Done()
	logger.Info("data ingestion shutting down")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envOrDefaultInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}
