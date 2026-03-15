package risk

import (
	"context"
	"fmt"
	"path"
	"time"

	"autonomy-platform/internal/config"
	"autonomy-platform/internal/models"
)

// CheckFunc is the signature for every pre-trade check.
type CheckFunc func(ctx context.Context, order *models.ProposedOrder, state *State, policy *config.Policy) CheckResultDetail

type namedCheck struct {
	Name string
	Func CheckFunc
}

// allChecks returns checks in evaluation order.
// Cheapest/most-likely-to-fail first to minimize wasted work.
// All checks run regardless (no short-circuit) for complete audit trail.
func allChecks() []namedCheck {
	return []namedCheck{
		{"system_mode", checkSystemMode},
		{"strategy_halted", checkStrategyNotHalted},
		{"venue_halted", checkVenueNotHalted},
		{"market_allowed", checkMarketAllowed},
		{"data_freshness", checkDataFreshness},
		{"price_sanity", checkPriceSanity},
		{"spread_check", checkSpread},
		{"per_trade_size", checkPerTradeSize},
		{"per_trade_notional", checkPerTradeNotional},
		{"position_limit", checkPositionLimit},
		{"concentration", checkConcentration},
		{"strategy_daily_loss", checkStrategyDailyLoss},
		{"strategy_order_freq", checkStrategyOrderFrequency},
		{"strategy_consec_loss", checkStrategyConsecutiveLosses},
		{"venue_exposure", checkVenueExposure},
		{"global_exposure", checkGlobalExposure},
		{"global_daily_loss", checkGlobalDailyLoss},
		{"global_drawdown", checkGlobalDrawdown},
		{"duplicate_order", checkDuplicateOrder},
		{"fat_finger", checkFatFinger},
	}
}

// RunAllChecks evaluates every check and returns all results.
func RunAllChecks(ctx context.Context, order *models.ProposedOrder, state *State, policy *config.Policy) []CheckResultDetail {
	checks := allChecks()
	results := make([]CheckResultDetail, 0, len(checks))
	for _, c := range checks {
		r := c.Func(ctx, order, state, policy)
		r.Name = c.Name
		results = append(results, r)
	}
	return results
}

func pass() CheckResultDetail { return CheckResultDetail{Passed: true} }
func fail(detail string) CheckResultDetail {
	return CheckResultDetail{Passed: false, Detail: detail}
}

func checkSystemMode(_ context.Context, _ *models.ProposedOrder, state *State, _ *config.Policy) CheckResultDetail {
	if state.SystemMode != "normal" {
		return fail(fmt.Sprintf("system in %s mode", state.SystemMode))
	}
	return pass()
}

func checkStrategyNotHalted(_ context.Context, order *models.ProposedOrder, state *State, _ *config.Policy) CheckResultDetail {
	ss, exists := state.Strategies[order.StrategyID]
	if exists && ss.Halted {
		return fail(fmt.Sprintf("strategy halted: %s", ss.HaltReason))
	}
	return pass()
}

func checkVenueNotHalted(_ context.Context, order *models.ProposedOrder, state *State, _ *config.Policy) CheckResultDetail {
	vs, exists := state.Venues[order.Venue]
	if exists && vs.Halted {
		return fail("venue halted")
	}
	return pass()
}

func checkMarketAllowed(_ context.Context, order *models.ProposedOrder, _ *State, policy *config.Policy) CheckResultDetail {
	patterns, exists := policy.AllowedMarkets[order.Venue]
	if !exists {
		return fail(fmt.Sprintf("venue %s not in allowed markets", order.Venue))
	}
	for _, pattern := range patterns {
		if pattern == "*" {
			return pass()
		}
		matched, _ := path.Match(pattern, order.MarketID)
		if matched {
			return pass()
		}
	}
	return fail(fmt.Sprintf("market %s not in allowed list for venue %s", order.MarketID, order.Venue))
}

func checkDataFreshness(_ context.Context, order *models.ProposedOrder, state *State, policy *config.Policy) CheckResultDetail {
	key := order.Venue + ":" + order.MarketID
	md, exists := state.MarketData[key]
	if !exists {
		return fail("no market data available")
	}
	age := time.Since(md.UpdatedAt).Seconds()
	maxAge := float64(policy.DataQuality.MaxDataAgeSeconds)
	if age > maxAge {
		return fail(fmt.Sprintf("data age %.1fs exceeds %.0fs limit", age, maxAge))
	}
	return pass()
}

func checkPriceSanity(_ context.Context, order *models.ProposedOrder, _ *State, policy *config.Policy) CheckResultDetail {
	if order.PriceMicros < policy.PerTrade.MinPriceMicros {
		return fail(fmt.Sprintf("price %d below minimum %d", order.PriceMicros, policy.PerTrade.MinPriceMicros))
	}
	if order.PriceMicros > policy.PerTrade.MaxPriceMicros {
		return fail(fmt.Sprintf("price %d above maximum %d", order.PriceMicros, policy.PerTrade.MaxPriceMicros))
	}
	return pass()
}

func checkSpread(_ context.Context, order *models.ProposedOrder, state *State, policy *config.Policy) CheckResultDetail {
	key := order.Venue + ":" + order.MarketID
	md, exists := state.MarketData[key]
	if !exists {
		return CheckResultDetail{Name: "spread_check", Passed: true, Detail: "no data, skipped"}
	}
	spreadBps := md.SpreadBps()
	if spreadBps > policy.PerTrade.MaxSpreadBps {
		return fail(fmt.Sprintf("spread %d bps exceeds %d bps limit", spreadBps, policy.PerTrade.MaxSpreadBps))
	}
	return pass()
}

func checkPerTradeSize(_ context.Context, order *models.ProposedOrder, _ *State, policy *config.Policy) CheckResultDetail {
	if order.Quantity > policy.PerTrade.MaxQuantity {
		return fail(fmt.Sprintf("quantity %d exceeds %d limit", order.Quantity, policy.PerTrade.MaxQuantity))
	}
	return pass()
}

func checkPerTradeNotional(_ context.Context, order *models.ProposedOrder, _ *State, policy *config.Policy) CheckResultDetail {
	if order.NotionalMicros > policy.PerTrade.MaxNotionalMicros {
		return fail(fmt.Sprintf("notional %d exceeds %d limit", order.NotionalMicros, policy.PerTrade.MaxNotionalMicros))
	}
	return pass()
}

func checkPositionLimit(_ context.Context, order *models.ProposedOrder, state *State, policy *config.Policy) CheckResultDetail {
	key := order.Venue + ":" + order.MarketID + ":" + order.StrategyID
	ms := state.Markets[key]
	currentNotional := int64(0)
	if ms != nil {
		currentNotional = int64(ms.PositionNotional)
	}
	newNotional := currentNotional + order.NotionalMicros
	if newNotional > policy.PerPosition.MaxNotionalMicros {
		return fail(fmt.Sprintf("position would be %d, exceeds %d limit", newNotional, policy.PerPosition.MaxNotionalMicros))
	}
	return pass()
}

func checkConcentration(_ context.Context, order *models.ProposedOrder, state *State, policy *config.Policy) CheckResultDetail {
	if state.TotalExposure == 0 {
		return pass() // no existing exposure, concentration is N/A
	}
	key := order.Venue + ":" + order.MarketID + ":" + order.StrategyID
	ms := state.Markets[key]
	currentNotional := int64(0)
	if ms != nil {
		currentNotional = int64(ms.PositionNotional)
	}
	newNotional := currentNotional + order.NotionalMicros
	totalWithNew := int64(state.TotalExposure) + order.NotionalMicros
	pct := float64(newNotional) / float64(totalWithNew) * 100
	if pct > policy.PerPosition.MaxConcentrationPct {
		return fail(fmt.Sprintf("concentration %.1f%% exceeds %.1f%% limit", pct, policy.PerPosition.MaxConcentrationPct))
	}
	return pass()
}

func checkStrategyDailyLoss(_ context.Context, order *models.ProposedOrder, state *State, policy *config.Policy) CheckResultDetail {
	ss, exists := state.Strategies[order.StrategyID]
	if !exists {
		return pass()
	}
	if int64(ss.DailyPnL) < -policy.PerStrategy.MaxDailyLossMicros {
		return fail(fmt.Sprintf("strategy daily loss %s exceeds limit", ss.DailyPnL.String()))
	}
	return pass()
}

func checkStrategyOrderFrequency(_ context.Context, order *models.ProposedOrder, state *State, policy *config.Policy) CheckResultDetail {
	times, exists := state.RecentOrderTimes[order.StrategyID]
	if !exists {
		return pass()
	}
	oneMinuteAgo := time.Now().Add(-1 * time.Minute)
	count := int32(0)
	for _, t := range times {
		if t.After(oneMinuteAgo) {
			count++
		}
	}
	if count >= policy.PerStrategy.MaxOrdersPerMinute {
		return fail(fmt.Sprintf("%d orders/min exceeds %d limit", count, policy.PerStrategy.MaxOrdersPerMinute))
	}
	return pass()
}

func checkStrategyConsecutiveLosses(_ context.Context, order *models.ProposedOrder, state *State, policy *config.Policy) CheckResultDetail {
	ss, exists := state.Strategies[order.StrategyID]
	if !exists {
		return pass()
	}
	if ss.ConsecutiveLosses >= policy.PerStrategy.MaxConsecutiveLosses {
		return fail(fmt.Sprintf("%d consecutive losses exceeds %d limit", ss.ConsecutiveLosses, policy.PerStrategy.MaxConsecutiveLosses))
	}
	return pass()
}

func checkVenueExposure(_ context.Context, order *models.ProposedOrder, state *State, policy *config.Policy) CheckResultDetail {
	vl, exists := policy.PerVenue[order.Venue]
	if !exists {
		return pass() // no venue-specific limit configured
	}
	vs := state.Venues[order.Venue]
	currentExposure := int64(0)
	if vs != nil {
		currentExposure = int64(vs.Exposure)
	}
	if currentExposure+order.NotionalMicros > vl.MaxExposureMicros {
		return fail(fmt.Sprintf("venue exposure would be %d, exceeds %d limit", currentExposure+order.NotionalMicros, vl.MaxExposureMicros))
	}
	return pass()
}

func checkGlobalExposure(_ context.Context, order *models.ProposedOrder, state *State, policy *config.Policy) CheckResultDetail {
	newExposure := int64(state.TotalExposure) + order.NotionalMicros
	if newExposure > policy.Global.MaxTotalExposureMicros {
		return fail(fmt.Sprintf("global exposure would be %d, exceeds %d limit", newExposure, policy.Global.MaxTotalExposureMicros))
	}
	return pass()
}

func checkGlobalDailyLoss(_ context.Context, _ *models.ProposedOrder, state *State, policy *config.Policy) CheckResultDetail {
	if int64(state.DailyPnL) < -policy.Global.MaxDailyLossMicros {
		return fail(fmt.Sprintf("global daily loss %s exceeds limit", state.DailyPnL.String()))
	}
	return pass()
}

func checkGlobalDrawdown(_ context.Context, _ *models.ProposedOrder, state *State, policy *config.Policy) CheckResultDetail {
	if state.PeakEquity == 0 {
		return pass()
	}
	drawdownPct := float64(state.PeakEquity-state.CurrentEquity) / float64(state.PeakEquity) * 100
	if drawdownPct >= policy.Global.MaxDrawdownPct {
		return fail(fmt.Sprintf("drawdown %.2f%% exceeds %.2f%% limit", drawdownPct, policy.Global.MaxDrawdownPct))
	}
	return pass()
}

func checkDuplicateOrder(_ context.Context, order *models.ProposedOrder, state *State, _ *config.Policy) CheckResultDetail {
	key := order.IdempotencyKey()
	lastSeen, exists := state.RecentOrderKeys[key]
	if exists && time.Since(lastSeen) < 30*time.Second {
		return fail("duplicate order within 30s window")
	}
	return pass()
}

func checkFatFinger(_ context.Context, order *models.ProposedOrder, state *State, _ *config.Policy) CheckResultDetail {
	notionals := state.RecentNotionals[order.StrategyID]
	if len(notionals) < 3 {
		return pass() // not enough history for fat-finger detection
	}
	// Compute average of last 10 orders
	recent := notionals
	if len(recent) > 10 {
		recent = recent[len(recent)-10:]
	}
	var sum int64
	for _, n := range recent {
		sum += n
	}
	avg := sum / int64(len(recent))
	if avg > 0 && order.NotionalMicros > avg*5 {
		return fail(fmt.Sprintf("notional %d is >5x recent average %d", order.NotionalMicros, avg))
	}
	return pass()
}
