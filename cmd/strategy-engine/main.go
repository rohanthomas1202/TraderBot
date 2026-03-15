package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"autonomy-platform/internal/events"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/data"
	"autonomy-platform/services/strategy"

	"github.com/nats-io/nats.go"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	logger := slog.Default().With("service", "strategy-engine")

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

	// Data source — subscribe to market data from NATS
	// For Phase 1, we also run a local data ingestion to populate the cache
	ingestion := data.NewIngestion(publisher)
	go ingestion.RunMockFeed(ctx, 1*time.Second)

	// Create strategy engine with simple momentum strategy
	// The evaluator is a stub that always approves — risk engine integration via gRPC is next.
	strategyID := envOrDefault("STRATEGY", "simple-momentum")
	engine := strategy.NewEngine(
		strategyID,
		"mock",
		strategy.SimpleMomentum(),
		&stubEvaluator{logger: logger},
		publisher,
	)

	intervalSec := envOrDefaultInt("SIGNAL_INTERVAL_SEC", 5)
	logger.Info("strategy engine ready",
		"strategy", strategyID,
		"interval_sec", intervalSec,
	)

	// Run signal loop
	engine.RunSignalLoop(ctx, time.Duration(intervalSec)*time.Second, func() map[string]*models.MarketData {
		return ingestion.GetLatest()
	})
}

// stubEvaluator is a placeholder until gRPC client to risk engine is wired.
type stubEvaluator struct {
	logger *slog.Logger
}

func (s *stubEvaluator) EvaluateOrder(ctx context.Context, order *models.ProposedOrder) (bool, error) {
	s.logger.Info("stub evaluator: would send to risk engine",
		"trace_id", order.TraceID,
		"market", order.MarketID,
		"side", order.Side,
		"notional", models.Money(order.NotionalMicros).String(),
	)
	return false, nil // deny by default until risk engine is connected
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
