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

	// Run mock data feed
	go ingestion.RunMockFeed(ctx, time.Duration(intervalMs)*time.Millisecond)

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
