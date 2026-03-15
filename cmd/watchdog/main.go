package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"autonomy-platform/gen/executionpb"
	"autonomy-platform/gen/watchdogpb"
	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/events"
	"autonomy-platform/internal/grpcauth"
	"autonomy-platform/internal/health"
	"autonomy-platform/internal/logging"
	"autonomy-platform/services/watchdog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	logger := logging.SetupLogger("watchdog")

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

	// Connect to execution engine for kill switch actions
	execAddr := envOrDefault("EXECUTION_ENGINE_ADDR", "localhost:50040")
	execConn, err := grpc.NewClient(execAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpcauth.CallerIdentityInterceptor("watchdog"),
	)
	if err != nil {
		logger.Error("failed to connect to execution engine", "addr", execAddr, "error", err)
		os.Exit(1)
	}
	defer execConn.Close()

	execClient := executionpb.NewExecutionEngineClient(execConn)
	execControl := watchdog.NewGRPCExecControl(execClient)
	riskControl := watchdog.NewGRPCRiskControl()

	// Create kill switch manager with gRPC clients
	killMgr := watchdog.NewKillSwitchManager(dbPool, execControl, riskControl, publisher, auditor)

	// Load any active halts from DB (survive restarts)
	if err := killMgr.LoadActiveHalts(ctx); err != nil {
		logger.Error("failed to load active halts", "error", err)
		os.Exit(1)
	}

	// Start health endpoint
	healthPort := envOrDefault("HEALTH_PORT", "50056")
	health.New(healthPort, "watchdog", func() error {
		return dbPool.Ping(ctx)
	}).Start()

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

	// Start gRPC server
	grpcPort := envOrDefault("GRPC_PORT", "50055")
	lis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		logger.Error("failed to listen", "port", grpcPort, "error", err)
		os.Exit(1)
	}

	allowedCallers := grpcauth.AllowedCallers{
		"/watchdog.Watchdog/TriggerKillSwitch": {"risk-engine", "trade-ctl"},
		"/watchdog.Watchdog/Heartbeat":         {"execution-engine", "risk-engine", "strategy-engine"},
		"/watchdog.Watchdog/GetSystemStatus":   {"*"},
		"/watchdog.Watchdog/AcknowledgeHalt":   {"trade-ctl"},
		"/watchdog.Watchdog/ResumeTrading":     {"trade-ctl"},
	}

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(grpcauth.UnaryCallerInterceptor(allowedCallers)))
	watchdogpb.RegisterWatchdogServer(grpcServer, watchdog.NewGRPCServer(killMgr, dms))

	go func() {
		logger.Info("gRPC server starting", "port", grpcPort)
		if err := grpcServer.Serve(lis); err != nil {
			logger.Error("gRPC server failed", "error", err)
		}
	}()

	logger.Info("watchdog fully wired",
		"grpc_port", grpcPort,
		"execution_engine", execAddr,
	)

	<-ctx.Done()
	logger.Info("watchdog shutting down")
	grpcServer.GracefulStop()
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
