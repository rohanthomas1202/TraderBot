package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"autonomy-platform/gen/watchdogpb"
	"autonomy-platform/internal/events"
	"autonomy-platform/internal/health"
	"autonomy-platform/internal/logging"
	"autonomy-platform/services/dashboard"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	logger := logging.SetupLogger("dashboard")

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

	subscriber, err := events.NewSubscriber(nc)
	if err != nil {
		logger.Error("failed to create subscriber", "error", err)
		os.Exit(1)
	}

	// Connect to Watchdog gRPC
	watchdogAddr := envOrDefault("WATCHDOG_ADDR", "localhost:50055")
	watchdogConn, err := grpc.NewClient(watchdogAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger.Error("failed to connect to watchdog", "error", err)
		os.Exit(1)
	}
	defer watchdogConn.Close()
	watchdogClient := watchdogpb.NewWatchdogClient(watchdogConn)

	apiKey := envOrDefault("DASHBOARD_API_KEY", "localdev")
	port := envOrDefault("DASHBOARD_PORT", "8080")

	// Create hub and NATS bridge
	hub := dashboard.NewHub(logger)
	go hub.Run(ctx)

	bridge := dashboard.NewNATSBridge(subscriber, hub, logger)
	if err := bridge.Start(); err != nil {
		logger.Error("failed to start NATS bridge", "error", err)
		os.Exit(1)
	}

	// Create server
	srv := dashboard.NewServer(dbPool, hub, watchdogClient, apiKey, logger)

	// Health endpoint
	health.New("50081", "dashboard").Start()

	// Start HTTP server
	httpServer := &http.Server{
		Addr:    ":" + port,
		Handler: srv.Handler(),
	}

	go func() {
		logger.Info("dashboard starting", "port", port, "url", "http://localhost:"+port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("dashboard server failed", "error", err)
		}
	}()

	logger.Info("dashboard fully wired",
		"port", port,
		"watchdog", watchdogAddr,
	)

	<-ctx.Done()
	logger.Info("dashboard shutting down")
	httpServer.Close()
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
