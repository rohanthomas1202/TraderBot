package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"autonomy-platform/gen/executionpb"
	"autonomy-platform/gen/riskpb"
	"autonomy-platform/internal/events"
	"autonomy-platform/internal/grpcauth"
	"autonomy-platform/internal/health"
	"autonomy-platform/internal/logging"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/strategy"

	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	logger := logging.SetupLogger("strategy-engine")

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

	// Subscribe to market data from NATS (published by data-ingestion service)
	subscriber, err := events.NewSubscriber(nc)
	if err != nil {
		logger.Error("failed to create event subscriber", "error", err)
		os.Exit(1)
	}

	dataCache := &marketDataCache{data: make(map[string]*models.MarketData)}
	_, err = subscriber.Subscribe(events.SubjectMarketData+".>", "strategy-engine-market-data", func(msg *nats.Msg) {
		var mde events.MarketDataEvent
		if err := json.Unmarshal(msg.Data, &mde); err != nil {
			logger.Error("failed to unmarshal market data", "error", err)
			msg.Nak()
			return
		}
		dataCache.update(mde.Venue, mde.MarketID, &models.MarketData{
			Venue:           mde.Venue,
			MarketID:        mde.MarketID,
			BidPriceMicros:  mde.BidPriceMicros,
			AskPriceMicros:  mde.AskPriceMicros,
			LastPriceMicros: mde.LastPriceMicros,
			BidDepth:        mde.BidDepth,
			AskDepth:        mde.AskDepth,
			UpdatedAt:       mde.Timestamp,
		})
		msg.Ack()
	})
	if err != nil {
		logger.Error("failed to subscribe to market data", "error", err)
		os.Exit(1)
	}

	// Start health endpoint
	healthPort := envOrDefault("HEALTH_PORT", "50031")
	health.New(healthPort, "strategy-engine").Start()

	// Connect to risk engine via gRPC
	riskAddr := envOrDefault("RISK_ENGINE_ADDR", "localhost:50020")
	riskConn, err := grpc.NewClient(riskAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpcauth.CallerIdentityInterceptor("strategy-engine"),
	)
	if err != nil {
		logger.Error("failed to connect to risk engine", "addr", riskAddr, "error", err)
		os.Exit(1)
	}
	defer riskConn.Close()

	// Connect to execution engine via gRPC
	execAddr := envOrDefault("EXECUTION_ENGINE_ADDR", "localhost:50040")
	execConn, err := grpc.NewClient(execAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpcauth.CallerIdentityInterceptor("strategy-engine"),
	)
	if err != nil {
		logger.Error("failed to connect to execution engine", "addr", execAddr, "error", err)
		os.Exit(1)
	}
	defer execConn.Close()

	evaluator := strategy.NewGRPCEvaluator(
		riskpb.NewRiskEngineClient(riskConn),
		executionpb.NewExecutionEngineClient(execConn),
	)

	// Create strategy engine with simple momentum strategy
	strategyID := envOrDefault("STRATEGY", "simple-momentum")
	engine := strategy.NewEngine(
		strategyID,
		"mock",
		strategy.SimpleMomentum(),
		evaluator,
		publisher,
	)

	intervalSec := envOrDefaultInt("SIGNAL_INTERVAL_SEC", 5)
	logger.Info("strategy engine ready",
		"strategy", strategyID,
		"interval_sec", intervalSec,
		"risk_engine", riskAddr,
		"execution_engine", execAddr,
	)

	// Run signal loop
	engine.RunSignalLoop(ctx, time.Duration(intervalSec)*time.Second, func() map[string]*models.MarketData {
		return dataCache.snapshot()
	})
}

// marketDataCache is a thread-safe cache of latest market data from NATS.
type marketDataCache struct {
	mu   sync.RWMutex
	data map[string]*models.MarketData
}

func (c *marketDataCache) update(venue, marketID string, md *models.MarketData) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[venue+":"+marketID] = md
}

func (c *marketDataCache) snapshot() map[string]*models.MarketData {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap := make(map[string]*models.MarketData, len(c.data))
	for k, v := range c.data {
		snap[k] = v
	}
	return snap
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
