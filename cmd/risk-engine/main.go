package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"autonomy-platform/gen/riskpb"
	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/config"
	"autonomy-platform/internal/events"
	"autonomy-platform/internal/grpcauth"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/risk"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	logger := slog.Default().With("service", "risk-engine")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Load policy
	policyPath := envOrDefault("POLICY_FILE", "./policies/paper.yaml")
	policy, err := config.LoadPolicy(policyPath)
	if err != nil {
		logger.Error("failed to load policy", "error", err)
		os.Exit(1)
	}
	logger.Info("policy loaded", "mode", policy.Mode, "hash", policy.ConfigHash())

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
	auditor := audit.NewLogger("risk-engine", dbPool)

	// Create and initialize risk engine
	hmacKey := []byte(envOrDefault("HMAC_KEY", "phase1-paper-trading-hmac-key-not-for-production"))
	engine := risk.NewEngine(dbPool, publisher, auditor, policy, hmacKey)

	if err := engine.LoadState(ctx); err != nil {
		logger.Error("failed to load risk state", "error", err)
		os.Exit(1)
	}

	// Subscribe to market data events to update freshness cache
	subscriber, err := events.NewSubscriber(nc)
	if err != nil {
		logger.Error("failed to create event subscriber", "error", err)
		os.Exit(1)
	}

	_, err = subscriber.Subscribe(events.SubjectMarketData+".>", "risk-engine-market-data", func(msg *nats.Msg) {
		var mde events.MarketDataEvent
		if err := json.Unmarshal(msg.Data, &mde); err != nil {
			logger.Error("failed to unmarshal market data event", "error", err)
			msg.Nak()
			return
		}

		engine.UpdateMarketData(&models.MarketData{
			Venue:           mde.Venue,
			MarketID:        mde.MarketID,
			BidPriceMicros:  mde.BidPriceMicros,
			AskPriceMicros:  mde.AskPriceMicros,
			LastPriceMicros: mde.LastPriceMicros,
			BidDepth:        mde.BidDepth,
			AskDepth:        mde.AskDepth,
			UpdatedAt:       mde.Timestamp,
		})

		logger.Debug("market data updated",
			"venue", mde.Venue,
			"market_id", mde.MarketID,
			"mid", (mde.BidPriceMicros+mde.AskPriceMicros)/2,
			"age_ms", time.Since(mde.Timestamp).Milliseconds(),
		)

		msg.Ack()
	})
	if err != nil {
		logger.Error("failed to subscribe to market data", "error", err)
		os.Exit(1)
	}

	// Subscribe to kill switch events to update system mode
	_, err = subscriber.Subscribe(events.SubjectKillActivated+".>", "risk-engine-kill-switch", func(msg *nats.Msg) {
		var kse events.KillSwitchEvent
		if err := json.Unmarshal(msg.Data, &kse); err != nil {
			logger.Error("failed to unmarshal kill switch event", "error", err)
			msg.Nak()
			return
		}
		engine.SetSystemMode(kse.Level)
		logger.Warn("system mode updated from kill switch event", "level", kse.Level, "scope", kse.Scope)
		msg.Ack()
	})
	if err != nil {
		logger.Error("failed to subscribe to kill switch events", "error", err)
		os.Exit(1)
	}

	// Start gRPC server
	grpcPort := envOrDefault("GRPC_PORT", "50020")
	lis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		logger.Error("failed to listen", "port", grpcPort, "error", err)
		os.Exit(1)
	}

	allowedCallers := grpcauth.AllowedCallers{
		"/risk.RiskEngine/EvaluateOrder": {"strategy-engine"},
		"/risk.RiskEngine/ReportFill":    {"execution-engine"},
		"/risk.RiskEngine/GetRiskState":  {"*"},
		"/risk.RiskEngine/UpdateLimit":   {"trade-ctl"},
	}

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(grpcauth.UnaryCallerInterceptor(allowedCallers)))
	riskpb.RegisterRiskEngineServer(grpcServer, risk.NewGRPCServer(engine))

	go func() {
		logger.Info("gRPC server starting", "port", grpcPort)
		if err := grpcServer.Serve(lis); err != nil {
			logger.Error("gRPC server failed", "error", err)
		}
	}()

	logger.Info("risk engine ready",
		"mode", policy.Mode,
		"system_mode", engine.GetState().SystemMode,
		"grpc_port", grpcPort,
	)

	<-ctx.Done()
	logger.Info("risk engine shutting down")
	grpcServer.GracefulStop()
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
