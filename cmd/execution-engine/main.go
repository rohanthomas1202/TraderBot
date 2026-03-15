package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/events"
	"autonomy-platform/services/execution"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	logger := slog.Default().With("service", "execution-engine")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Connect to Postgres
	dbPool, err := pgxpool.New(ctx, envOrDefault("POSTGRES_URL", "postgres://trader:localdev@localhost:5432/autonomy?sslmode=disable"))
	if err != nil {
		logger.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

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

	// Create audit logger
	auditor := audit.NewLogger("execution-engine", dbPool)

	// Create mock venue adapter (paper trading)
	venue := execution.NewMockAdapter(envOrDefault("MOCK_EXCHANGE_ADDR", "localhost:50060"))

	// Create execution engine
	hmacKey := []byte(envOrDefault("HMAC_KEY", "phase1-paper-trading-hmac-key-not-for-production"))
	engine := execution.NewEngine(dbPool, venue, publisher, auditor, hmacKey)

	// Load open orders from DB (crash recovery)
	if err := engine.LoadOpenOrders(ctx); err != nil {
		logger.Error("failed to load open orders", "error", err)
		os.Exit(1)
	}

	logger.Info("execution engine ready", "mode", envOrDefault("EXECUTION_MODE", "paper"))

	// TODO: start gRPC server to accept SubmitOrder, CancelOrder, CancelAll calls
	// TODO: start fill polling loop
	// TODO: start heartbeat reporting to watchdog

	<-ctx.Done()
	logger.Info("execution engine shutting down")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
