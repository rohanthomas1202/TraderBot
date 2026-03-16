package backtest

import (
	"context"
	"fmt"
	"time"

	"autonomy-platform/internal/config"
	"autonomy-platform/internal/models"
	"autonomy-platform/services/risk"
	"autonomy-platform/services/strategy"

	"github.com/google/uuid"
)

// RunConfig configures a backtest run.
type RunConfig struct {
	StrategyName   string
	Venue          string
	Policy         *config.Policy
	InitialCapital int64 // micros
	FillMode       string
	Ticks          []Tick // pre-loaded market data ticks
}

// RunResult holds the output of a backtest.
type RunResult struct {
	Config      RunConfig
	Metrics     BacktestMetrics
	Trades      []Trade
	EquityCurve []int64
	Duration    time.Duration
}

// Run executes a backtest with the given configuration.
func Run(ctx context.Context, cfg RunConfig) (*RunResult, error) {
	signalFn, err := GetStrategy(cfg.StrategyName)
	if err != nil {
		return nil, err
	}

	filler := selectFiller(cfg.FillMode)
	clock := NewSimClock(time.Now())

	state := risk.NewEmptyState()
	state.NowFunc = clock.Now
	state.CurrentEquity = models.Money(cfg.InitialCapital)
	state.PeakEquity = models.Money(cfg.InitialCapital)

	var trades []Trade
	equityCurve := []int64{cfg.InitialCapital}
	equity := cfg.InitialCapital

	start := time.Now()

	for _, tick := range cfg.Ticks {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		clock.Set(tick.Timestamp)

		// Update market data in risk state
		for _, md := range tick.Data {
			key := md.Venue + ":" + md.MarketID
			state.MarketData[key] = md
		}

		// Generate signals
		signals := signalFn(tick.Data)

		for _, sig := range signals {
			order := buildOrder(sig, signalFn, cfg, tick.Timestamp)

			// Run risk checks
			results := risk.RunAllChecks(ctx, order, state, cfg.Policy)
			allPassed := true
			for _, r := range results {
				if !r.Passed {
					allPassed = false
					break
				}
			}

			if !allPassed {
				continue
			}

			// Simulate fill
			md := tick.Data[order.Venue+":"+order.MarketID]
			filledQty, fillPrice := filler.SimulateFill(order, md)
			if filledQty == 0 {
				continue
			}

			// Compute P&L (simplified: for event contracts, P&L = fill value change)
			pnl := computePnL(order.Side, filledQty, fillPrice, state, order.Venue, order.MarketID, cfg.StrategyName)

			// Update state
			updateState(state, order, filledQty, fillPrice)

			trade := Trade{
				MarketID:    order.MarketID,
				Side:        string(order.Side),
				Quantity:    filledQty,
				PriceMicros: fillPrice,
				PnLMicros:   pnl,
			}
			trades = append(trades, trade)

			equity += pnl
			equityCurve = append(equityCurve, equity)

			state.DailyPnL += models.Money(pnl)
			state.CurrentEquity = models.Money(equity)
			if models.Money(equity) > state.PeakEquity {
				state.PeakEquity = models.Money(equity)
			}
		}
	}

	metrics := ComputeFromTrades(trades, equityCurve, cfg.InitialCapital)

	return &RunResult{
		Config:      cfg,
		Metrics:     metrics,
		Trades:      trades,
		EquityCurve: equityCurve,
		Duration:    time.Since(start),
	}, nil
}

func buildOrder(sig strategy.Signal, _ strategy.SignalFunc, cfg RunConfig, ts time.Time) *models.ProposedOrder {
	return &models.ProposedOrder{
		TraceID:        uuid.New().String(),
		StrategyID:     cfg.StrategyName,
		Venue:          cfg.Venue,
		MarketID:       sig.MarketID,
		Side:           sig.Side,
		Quantity:       sig.Quantity,
		PriceMicros:    sig.PriceMicros,
		NotionalMicros: int64(sig.Quantity) * sig.PriceMicros,
		ProposedAt:     ts,
	}
}

func computePnL(side models.Side, qty int32, fillPrice int64, state *risk.State, venue, marketID, strategyID string) int64 {
	key := venue + ":" + marketID + ":" + strategyID
	ms := state.Markets[key]
	if ms == nil {
		return 0 // opening position, no P&L yet
	}

	// If closing/reducing a position, compute P&L
	if side == models.SideSell && ms.PositionContracts > 0 {
		closeQty := qty
		if closeQty > ms.PositionContracts {
			closeQty = ms.PositionContracts
		}
		avgEntry := int64(0)
		if ms.PositionContracts > 0 {
			avgEntry = int64(ms.PositionNotional) / int64(ms.PositionContracts)
		}
		return int64(closeQty) * (fillPrice - avgEntry)
	}
	if side == models.SideBuy && ms.PositionContracts < 0 {
		closeQty := qty
		if int32(-ms.PositionContracts) < closeQty {
			closeQty = int32(-ms.PositionContracts)
		}
		avgEntry := int64(0)
		if ms.PositionContracts != 0 {
			avgEntry = int64(ms.PositionNotional) / int64(-ms.PositionContracts)
		}
		return int64(closeQty) * (avgEntry - fillPrice)
	}
	return 0
}

func updateState(state *risk.State, order *models.ProposedOrder, filledQty int32, fillPrice int64) {
	key := order.Venue + ":" + order.MarketID + ":" + order.StrategyID
	ms, exists := state.Markets[key]
	if !exists {
		ms = &risk.MarketState{StrategyID: order.StrategyID}
		state.Markets[key] = ms
	}

	fillNotional := models.Money(int64(filledQty) * fillPrice)
	if order.Side == models.SideBuy {
		ms.PositionContracts += filledQty
		ms.PositionNotional += fillNotional
	} else {
		ms.PositionContracts -= filledQty
		ms.PositionNotional -= fillNotional
	}
	state.TotalExposure = models.Money(int64(ms.PositionNotional.Abs()))

	// Update rate tracking
	state.RecentOrderTimes[order.StrategyID] = append(state.RecentOrderTimes[order.StrategyID], order.ProposedAt)
	state.RecentNotionals[order.StrategyID] = append(state.RecentNotionals[order.StrategyID], order.NotionalMicros)

	key2 := fmt.Sprintf("%s:%s:%s:%d:%d", order.StrategyID, order.MarketID, order.Side, order.Quantity, order.PriceMicros)
	state.RecentOrderKeys[key2] = order.ProposedAt
}

func selectFiller(mode string) FillSimulator {
	return &DeterministicFiller{}
}
