package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/events"
	"autonomy-platform/internal/ledger"
	"autonomy-platform/services/execution"
	"autonomy-platform/services/mockexchange"
	"autonomy-platform/services/risk"

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

	// Create order intent ledger
	intentLedger := ledger.NewLedger(dbPool)

	// Exposure limits from env (microdollars)
	limits := ledger.ExposureLimits{
		MaxPositionNotionalMicros: envOrInt64("MAX_POSITION_NOTIONAL", 10_000_000_000),  // $10,000
		MaxTotalExposureMicros:    envOrInt64("MAX_TOTAL_EXPOSURE", 50_000_000_000),      // $50,000
	}

	// Create paper venue adapter with configurable mock exchange
	mockCfg := mockexchange.Config{
		FillDelayMs:          envOrInt("MOCK_FILL_DELAY_MS", 100),
		FillProbability:      0.8,
		PartialFillProb:      0.2,
		RejectionRate:        0.0,
		InitialBalanceMicros: 100_000_000_000, // $100,000
	}
	venue := execution.NewPaperAdapter(mockCfg)

	// Create execution engine
	hmacKey := []byte(envOrDefault("HMAC_KEY", "phase1-paper-trading-hmac-key-not-for-production"))
	engine := execution.NewEngine(dbPool, venue, publisher, auditor, hmacKey, intentLedger, limits)

	// Load open orders from DB (crash recovery)
	if err := engine.LoadOpenOrders(ctx); err != nil {
		logger.Error("failed to load open orders", "error", err)
		os.Exit(1)
	}

	logger.Info("execution engine ready",
		"mode", envOrDefault("EXECUTION_MODE", "paper"),
		"open_orders", engine.OpenOrderCount(),
	)

	// Create a risk engine stub for fill reporting.
	// In production this would be a gRPC client; here we connect directly.
	riskCallback := func(ctx context.Context, fill *risk.FillReport) error {
		logger.Info("fill reported to risk",
			"trace_id", fill.TraceID,
			"market_id", fill.MarketID,
			"side", fill.Side,
			"quantity", fill.Quantity,
		)
		return nil
	}

	// Start fill polling loop
	pollInterval := time.Duration(envOrInt("FILL_POLL_INTERVAL_MS", 500)) * time.Millisecond
	go runFillPoller(ctx, engine, riskCallback, pollInterval, logger)

	// TODO: start gRPC server to accept SubmitOrder, CancelOrder, CancelAll calls
	// TODO: start heartbeat reporting to watchdog

	<-ctx.Done()
	logger.Info("execution engine shutting down")
}

func runFillPoller(ctx context.Context, engine *execution.Engine, riskCallback func(context.Context, *risk.FillReport) error, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	lastPoll := time.Now().Add(-1 * time.Minute) // start by looking back 1 minute

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollStart := time.Now()
			if err := engine.PollFills(ctx, lastPoll, riskCallback); err != nil {
				logger.Error("fill poll error", "error", err)
				continue
			}
			lastPoll = pollStart
		}
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envOrInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envOrInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
