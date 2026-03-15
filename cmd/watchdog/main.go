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
	"autonomy-platform/services/watchdog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	logger := slog.Default().With("service", "watchdog")

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

	auditor := audit.NewLogger("watchdog", dbPool)

	// Create kill switch manager.
	// In Phase 1, exec and risk controls are nil — we don't have gRPC clients yet.
	// The kill switch still persists events and publishes to NATS.
	// Services read system mode from the watchdog on startup and via heartbeat responses.
	killMgr := watchdog.NewKillSwitchManager(dbPool, nil, nil, publisher, auditor)

	// Load any active halts from DB (survive restarts)
	if err := killMgr.LoadActiveHalts(ctx); err != nil {
		logger.Error("failed to load active halts", "error", err)
		os.Exit(1)
	}

	// Create dead man's switch
	heartbeatSec := envOrDefaultInt("HEARTBEAT_INTERVAL_SEC", 10)
	graceMultiple := envOrDefaultInt("HEARTBEAT_GRACE_MULTIPLE", 3)
	dms := watchdog.NewDeadMansSwitch(killMgr, dbPool, time.Duration(heartbeatSec)*time.Second, graceMultiple)

	logger.Info("watchdog ready",
		"mode", killMgr.GetCurrentMode(),
		"heartbeat_interval", heartbeatSec,
		"grace_multiple", graceMultiple,
	)

	// Start dead man's switch monitor
	go dms.Monitor(ctx)

	// TODO: start gRPC server for TriggerKillSwitch, Heartbeat, GetSystemStatus
	// TODO: start auto-trigger monitor (reads risk state periodically)

	<-ctx.Done()
	logger.Info("watchdog shutting down")
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
