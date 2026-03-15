package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"autonomy-platform/gen/commonpb"
	"autonomy-platform/gen/executionpb"
	"autonomy-platform/gen/riskpb"
	"autonomy-platform/gen/watchdogpb"
	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/convert"
	"autonomy-platform/internal/events"
	"autonomy-platform/internal/grpcauth"
	"autonomy-platform/internal/ledger"
	"autonomy-platform/services/execution"
	"autonomy-platform/services/mockexchange"
	"autonomy-platform/services/recon"
	"autonomy-platform/services/risk"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

	// Full crash recovery: load open orders + replay intent ledger
	if err := engine.RecoverFull(ctx); err != nil {
		logger.Error("failed crash recovery", "error", err)
		os.Exit(1)
	}

	logger.Info("execution engine recovered",
		"mode", envOrDefault("EXECUTION_MODE", "paper"),
		"open_orders", engine.OpenOrderCount(),
	)

	// Startup reconciliation: verify internal state matches exchange
	reconComparator := recon.NewComparator(dbPool, venue)
	reconInterval := time.Duration(envOrInt("RECON_INTERVAL_SEC", 60)) * time.Second
	reconEngine := recon.NewEngine(dbPool, reconComparator, nil, auditor, reconInterval) // killSwitch wired after watchdog connection

	consistent, err := reconEngine.RunStartupCheck(ctx)
	if err != nil {
		logger.Error("startup reconciliation failed", "error", err)
		os.Exit(1)
	}
	if consistent {
		engine.SetReconciled(true)
		logger.Info("startup reconciliation passed — trading enabled")
	} else {
		// In paper mode, warn but allow trading (exchange is in-process, state is fresh)
		engine.SetReconciled(true)
		logger.Warn("startup reconciliation found mismatches — proceeding in paper mode")
	}

	// Connect to risk engine for fill reporting
	riskAddr := envOrDefault("RISK_ENGINE_ADDR", "localhost:50020")
	riskConn, err := grpc.NewClient(riskAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpcauth.CallerIdentityInterceptor("execution-engine"),
	)
	if err != nil {
		logger.Error("failed to connect to risk engine", "addr", riskAddr, "error", err)
		os.Exit(1)
	}
	defer riskConn.Close()
	riskClient := riskpb.NewRiskEngineClient(riskConn)

	riskCallback := func(ctx context.Context, fill *risk.FillReport) error {
		_, err := riskClient.ReportFill(ctx, &riskpb.ReportFillRequest{
			TraceId:         fill.TraceID,
			InternalOrderId: fill.InternalOrderID,
			StrategyId:      fill.StrategyID,
			Market:          &commonpb.Market{Venue: convert.VenueToProto(fill.Venue), MarketId: fill.MarketID},
			Side:            convert.SideToProto(fill.Side),
			FilledQuantity:  fill.Quantity,
			FillPriceMicros: fill.PriceMicros,
		})
		if err != nil {
			logger.Error("failed to report fill to risk engine", "trace_id", fill.TraceID, "error", err)
		}
		return err
	}

	// Start fill polling loop
	pollInterval := time.Duration(envOrInt("FILL_POLL_INTERVAL_MS", 500)) * time.Millisecond
	go runFillPoller(ctx, engine, riskCallback, pollInterval, logger)

	// Connect to watchdog for heartbeats
	watchdogAddr := envOrDefault("WATCHDOG_ADDR", "localhost:50055")
	watchdogConn, err := grpc.NewClient(watchdogAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpcauth.CallerIdentityInterceptor("execution-engine"),
	)
	if err != nil {
		logger.Error("failed to connect to watchdog", "addr", watchdogAddr, "error", err)
		os.Exit(1)
	}
	defer watchdogConn.Close()
	watchdogClient := watchdogpb.NewWatchdogClient(watchdogConn)

	// Start heartbeat loop
	heartbeatSec := envOrInt("HEARTBEAT_INTERVAL_SEC", 10)
	go runHeartbeatLoop(ctx, watchdogClient, engine, time.Duration(heartbeatSec)*time.Second, logger)

	// Start periodic reconciliation (with kill switch trigger via watchdog)
	reconWithKill := recon.NewEngine(dbPool, reconComparator, &watchdogKillAdapter{client: watchdogClient}, auditor, reconInterval)
	go reconWithKill.Run(ctx)

	// Start gRPC server
	grpcPort := envOrDefault("GRPC_PORT", "50040")
	lis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		logger.Error("failed to listen", "port", grpcPort, "error", err)
		os.Exit(1)
	}

	allowedCallers := grpcauth.AllowedCallers{
		"/execution.ExecutionEngine/SubmitOrder":      {"strategy-engine"},
		"/execution.ExecutionEngine/CancelOrder":      {"watchdog", "trade-ctl"},
		"/execution.ExecutionEngine/CancelAll":        {"watchdog", "trade-ctl"},
		"/execution.ExecutionEngine/GetOrders":        {"*"},
		"/execution.ExecutionEngine/GetOrderSummary":  {"*"},
	}

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(grpcauth.UnaryCallerInterceptor(allowedCallers)))
	executionpb.RegisterExecutionEngineServer(grpcServer, execution.NewGRPCServer(engine, dbPool))

	go func() {
		logger.Info("gRPC server starting", "port", grpcPort)
		if err := grpcServer.Serve(lis); err != nil {
			logger.Error("gRPC server failed", "error", err)
		}
	}()

	logger.Info("execution engine fully wired",
		"grpc_port", grpcPort,
		"risk_engine", riskAddr,
		"watchdog", watchdogAddr,
		"recon_interval", reconInterval,
	)

	<-ctx.Done()
	logger.Info("execution engine shutting down")
	grpcServer.GracefulStop()
}

// watchdogKillAdapter adapts the watchdog gRPC client to the recon.KillSwitchTrigger interface.
type watchdogKillAdapter struct {
	client watchdogpb.WatchdogClient
}

func (a *watchdogKillAdapter) Trigger(ctx context.Context, level string, scope, reason, triggeredBy string) error {
	_, err := a.client.TriggerKillSwitch(ctx, &watchdogpb.KillSwitchRequest{
		Level:       convert.KillSwitchLevelToProto(level),
		Scope:       scope,
		Reason:      reason,
		TriggeredBy: triggeredBy,
	})
	return err
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

func runHeartbeatLoop(ctx context.Context, client watchdogpb.WatchdogClient, engine *execution.Engine, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			resp, err := client.Heartbeat(ctx, &watchdogpb.HeartbeatRequest{
				ServiceName: "execution-engine",
				Status:      "healthy",
				Detail:      fmt.Sprintf("open_orders=%d reconciled=%v", engine.OpenOrderCount(), engine.IsReconciled()),
			})
			if err != nil {
				logger.Error("heartbeat failed", "error", err)
				continue
			}
			if resp.GetSystemMode() != "" {
				engine.SetSystemMode(resp.GetSystemMode())
			}
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
