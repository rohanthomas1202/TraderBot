package risk

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/config"
	"autonomy-platform/internal/events"
	"autonomy-platform/internal/models"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Engine is the merged risk + policy engine.
// It maintains risk state and evaluates orders against policy.
type Engine struct {
	db        *pgxpool.Pool
	publisher *events.Publisher
	auditor   *audit.Logger
	policy    *config.Policy
	hmacKey   []byte
	logger    *slog.Logger

	mu    sync.RWMutex
	state *State
}

// State holds the in-memory risk state, loaded from DB on startup
// and updated on every fill.
type State struct {
	SystemMode       string // "normal", "soft_pause", "cancel_only", "full_stop"
	TotalExposure    models.Money
	DailyPnL         models.Money
	PeakEquity       models.Money
	CurrentEquity    models.Money
	Strategies       map[string]*StrategyState
	Venues           map[string]*VenueState
	Markets          map[string]*MarketState

	// Rate tracking
	RecentOrderTimes map[string][]time.Time // strategy_id → recent order timestamps
	RecentOrderKeys  map[string]time.Time   // idempotency_key → last seen time
	RecentNotionals  map[string][]int64     // strategy_id → recent notional values

	// Market data cache (updated by data ingestion events)
	MarketData       map[string]*models.MarketData // venue:market_id → latest data
}

type StrategyState struct {
	DailyPnL          models.Money
	Exposure          models.Money
	DailyOrderCount   int32
	DailyTurnover     models.Money
	ConsecutiveLosses int32
	Halted            bool
	HaltReason        string
	OpenOrderCount    int32
}

type VenueState struct {
	DailyPnL models.Money
	Exposure models.Money
	Halted   bool
}

type MarketState struct {
	PositionContracts int32
	PositionNotional  models.Money
	StrategyID        string
}

func NewEngine(db *pgxpool.Pool, publisher *events.Publisher, auditor *audit.Logger, policy *config.Policy, hmacKey []byte) *Engine {
	return &Engine{
		db:        db,
		publisher: publisher,
		auditor:   auditor,
		policy:    policy,
		hmacKey:   hmacKey,
		logger:    slog.Default().With("service", "risk-engine"),
		state:     newEmptyState(),
	}
}

func newEmptyState() *State {
	return &State{
		SystemMode:       "normal",
		Strategies:       make(map[string]*StrategyState),
		Venues:           make(map[string]*VenueState),
		Markets:          make(map[string]*MarketState),
		RecentOrderTimes: make(map[string][]time.Time),
		RecentOrderKeys:  make(map[string]time.Time),
		RecentNotionals:  make(map[string][]int64),
		MarketData:       make(map[string]*models.MarketData),
	}
}

// LoadState rebuilds risk state from the database on startup.
func (e *Engine) LoadState(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Load positions
	rows, err := e.db.Query(ctx,
		`SELECT venue, market_id, strategy_id, net_quantity, notional_micros, realized_pnl_micros
		 FROM risk.positions WHERE net_quantity != 0`)
	if err != nil {
		return fmt.Errorf("load positions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var venue, marketID, strategyID string
		var qty int32
		var notional, pnl int64
		if err := rows.Scan(&venue, &marketID, &strategyID, &qty, &notional, &pnl); err != nil {
			return fmt.Errorf("scan position: %w", err)
		}
		key := venue + ":" + marketID + ":" + strategyID
		e.state.Markets[key] = &MarketState{
			PositionContracts: qty,
			PositionNotional:  models.Money(notional),
			StrategyID:        strategyID,
		}
		e.state.TotalExposure += models.Money(notional).Abs()
	}

	// Load today's daily stats
	today := time.Now().UTC().Format("2006-01-02")
	statsRows, err := e.db.Query(ctx,
		`SELECT scope, pnl_micros, turnover_micros, order_count, consecutive_losses
		 FROM risk.daily_stats WHERE date = $1`, today)
	if err != nil {
		return fmt.Errorf("load daily stats: %w", err)
	}
	defer statsRows.Close()

	for statsRows.Next() {
		var scope string
		var pnl, turnover int64
		var orderCount, consec int32
		if err := statsRows.Scan(&scope, &pnl, &turnover, &orderCount, &consec); err != nil {
			return fmt.Errorf("scan daily stat: %w", err)
		}
		if scope == "global" {
			e.state.DailyPnL = models.Money(pnl)
		}
		// Parse strategy/venue scopes and populate state...
	}

	// Load active halts from watchdog
	var mode string
	err = e.db.QueryRow(ctx,
		`SELECT COALESCE(
			(SELECT CASE level
				WHEN 'full_stop' THEN 'full_stop'
				WHEN 'cancel_only' THEN 'cancel_only'
				WHEN 'soft_pause' THEN 'soft_pause'
			 END
			 FROM watchdog.kill_switch_events
			 WHERE resumed = FALSE
			 ORDER BY CASE level
				WHEN 'full_stop' THEN 3
				WHEN 'cancel_only' THEN 2
				WHEN 'soft_pause' THEN 1
			 END DESC
			 LIMIT 1),
			'normal'
		)`).Scan(&mode)
	if err != nil {
		return fmt.Errorf("load system mode: %w", err)
	}
	e.state.SystemMode = mode

	e.logger.Info("risk state loaded",
		"system_mode", e.state.SystemMode,
		"total_exposure", e.state.TotalExposure.String(),
		"daily_pnl", e.state.DailyPnL.String(),
	)
	return nil
}

// EvaluateOrder runs all pre-trade checks and returns a signed approval or denial.
func (e *Engine) EvaluateOrder(ctx context.Context, order *models.ProposedOrder) (*Approval, error) {
	if err := order.Validate(); err != nil {
		return nil, fmt.Errorf("invalid order: %w", err)
	}

	e.mu.RLock()
	state := e.state // safe to read while holding RLock
	e.mu.RUnlock()

	// Run all checks — does NOT short-circuit, so audit log captures every result
	results := RunAllChecks(ctx, order, state, e.policy)

	allPassed := true
	var firstFailure string
	for _, r := range results {
		if !r.Passed {
			allPassed = false
			if firstFailure == "" {
				firstFailure = r.Name + ": " + r.Detail
			}
		}
	}

	decision := DecisionApproved
	if !allPassed {
		decision = DecisionDenied
	}

	now := time.Now().UTC()
	approval := &Approval{
		TraceID:          order.TraceID,
		Order:            order,
		Decision:         decision,
		Checks:           results,
		PolicyConfigHash: e.policy.ConfigHash(),
		DecidedAt:        now,
	}

	// Sign the approval with HMAC
	approval.HMACSignature = e.signApproval(approval)

	// Persist the decision
	checksJSON, _ := encodeChecks(results)
	stateJSON, _ := encodeState(state)
	_, err := e.db.Exec(ctx,
		`INSERT INTO risk.policy_decisions (trace_id, strategy_id, market_id, decision, checks_json, policy_config_hash, risk_state_snapshot, decided_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		order.TraceID, order.StrategyID, order.MarketID,
		string(decision), checksJSON, approval.PolicyConfigHash, stateJSON, now,
	)
	if err != nil {
		e.logger.Error("failed to persist policy decision", "trace_id", order.TraceID, "error", err)
		// Non-fatal: the decision still stands, but we have a gap in the DB audit trail.
		// The NATS event below provides a backup.
	}

	// Publish event
	if decision == DecisionApproved {
		e.publisher.Publish(events.SubjectOrderApproved+"."+order.StrategyID, events.OrderApprovedEvent{
			TraceID:          order.TraceID,
			PolicyConfigHash: approval.PolicyConfigHash,
			Timestamp:        now,
		})
		e.auditor.LogInfo(ctx, "order.approved", order.TraceID, map[string]interface{}{
			"strategy_id": order.StrategyID,
			"market_id":   order.MarketID,
			"notional":    order.NotionalMicros,
		})

		// Update rate tracking
		e.mu.Lock()
		e.state.RecentOrderTimes[order.StrategyID] = append(e.state.RecentOrderTimes[order.StrategyID], now)
		e.state.RecentOrderKeys[order.IdempotencyKey()] = now
		e.state.RecentNotionals[order.StrategyID] = append(e.state.RecentNotionals[order.StrategyID], order.NotionalMicros)
		e.mu.Unlock()
	} else {
		eventChecks := make([]events.CheckResult, 0)
		for _, r := range results {
			if !r.Passed {
				eventChecks = append(eventChecks, events.CheckResult{
					Name:   r.Name,
					Result: "fail",
					Detail: r.Detail,
				})
			}
		}
		e.publisher.Publish(events.SubjectOrderDenied+"."+order.StrategyID, events.OrderDeniedEvent{
			TraceID:      order.TraceID,
			DenyReason:   firstFailure,
			FailedChecks: eventChecks,
			Timestamp:    now,
		})
		e.auditor.LogInfo(ctx, "order.denied", order.TraceID, map[string]interface{}{
			"strategy_id": order.StrategyID,
			"reason":      firstFailure,
		})
	}

	return approval, nil
}

// UpdateMarketData is called when new market data arrives (via NATS subscription).
func (e *Engine) UpdateMarketData(data *models.MarketData) {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := data.Venue + ":" + data.MarketID
	e.state.MarketData[key] = data
}

// ReportFill updates risk state when a fill occurs.
func (e *Engine) ReportFill(ctx context.Context, fill *FillReport) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Update position
	key := fill.Venue + ":" + fill.MarketID + ":" + fill.StrategyID
	ms, exists := e.state.Markets[key]
	if !exists {
		ms = &MarketState{StrategyID: fill.StrategyID}
		e.state.Markets[key] = ms
	}

	fillNotional := models.Money(int64(fill.Quantity) * fill.PriceMicros)
	if fill.Side == models.SideBuy {
		ms.PositionContracts += fill.Quantity
		ms.PositionNotional += fillNotional
	} else {
		ms.PositionContracts -= fill.Quantity
		ms.PositionNotional -= fillNotional
	}

	// Update daily stats in DB
	today := time.Now().UTC().Format("2006-01-02")
	for _, scope := range []string{"global", "venue:" + fill.Venue, "strategy:" + fill.StrategyID} {
		_, err := e.db.Exec(ctx,
			`INSERT INTO risk.daily_stats (date, scope, fill_count, turnover_micros, updated_at)
			 VALUES ($1, $2, 1, $3, NOW())
			 ON CONFLICT (date, scope) DO UPDATE SET
			   fill_count = risk.daily_stats.fill_count + 1,
			   turnover_micros = risk.daily_stats.turnover_micros + $3,
			   updated_at = NOW()`,
			today, scope, int64(fillNotional.Abs()),
		)
		if err != nil {
			e.logger.Error("failed to update daily stats", "scope", scope, "error", err)
		}
	}

	// Update position in DB
	_, err := e.db.Exec(ctx,
		`INSERT INTO risk.positions (venue, market_id, strategy_id, net_quantity, notional_micros, updated_at)
		 VALUES ($1, $2, $3, $4, $5, NOW())
		 ON CONFLICT (venue, market_id, strategy_id) DO UPDATE SET
		   net_quantity = $4,
		   notional_micros = $5,
		   updated_at = NOW()`,
		fill.Venue, fill.MarketID, fill.StrategyID, ms.PositionContracts, int64(ms.PositionNotional),
	)
	if err != nil {
		e.logger.Error("failed to update position", "error", err)
	}

	return nil
}

// SetSystemMode is called by the watchdog when a kill switch changes the mode.
func (e *Engine) SetSystemMode(mode string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.SystemMode = mode
	e.logger.Warn("system mode changed", "mode", mode)
}

// GetState returns a snapshot of current risk state (for watchdog, CLI, etc.)
func (e *Engine) GetState() *State {
	e.mu.RLock()
	defer e.mu.RUnlock()
	// Return a reference — callers should not modify.
	// A deep copy would be safer but is not needed in Phase 1.
	return e.state
}

// signApproval produces HMAC-SHA256 over canonical order fields.
func (e *Engine) signApproval(a *Approval) []byte {
	mac := hmac.New(sha256.New, e.hmacKey)
	mac.Write([]byte(a.TraceID))
	mac.Write([]byte(a.Order.Venue))
	mac.Write([]byte(a.Order.MarketID))
	mac.Write([]byte(a.Order.Side))
	binary.Write(mac, binary.BigEndian, a.Order.Quantity)
	binary.Write(mac, binary.BigEndian, a.Order.PriceMicros)
	binary.Write(mac, binary.BigEndian, a.Order.NotionalMicros)
	mac.Write([]byte(a.Decision))
	binary.Write(mac, binary.BigEndian, a.DecidedAt.UnixMicro())
	return mac.Sum(nil)
}

// VerifyApproval checks an approval's HMAC. Used by execution engine.
func VerifyApproval(a *Approval, hmacKey []byte) bool {
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte(a.TraceID))
	mac.Write([]byte(a.Order.Venue))
	mac.Write([]byte(a.Order.MarketID))
	mac.Write([]byte(a.Order.Side))
	binary.Write(mac, binary.BigEndian, a.Order.Quantity)
	binary.Write(mac, binary.BigEndian, a.Order.PriceMicros)
	binary.Write(mac, binary.BigEndian, a.Order.NotionalMicros)
	mac.Write([]byte(a.Decision))
	binary.Write(mac, binary.BigEndian, a.DecidedAt.UnixMicro())
	expected := mac.Sum(nil)
	return hmac.Equal(a.HMACSignature, expected)
}

// ─── Types ───

type Decision string

const (
	DecisionApproved  Decision = "approved"
	DecisionDenied    Decision = "denied"
	DecisionEscalated Decision = "escalated"
)

type Approval struct {
	TraceID          string
	Order            *models.ProposedOrder
	Decision         Decision
	Checks           []CheckResultDetail
	PolicyConfigHash string
	DecidedAt        time.Time
	HMACSignature    []byte
}

type FillReport struct {
	TraceID         string
	InternalOrderID string
	StrategyID      string
	Venue           string
	MarketID        string
	Side            models.Side
	Quantity        int32
	PriceMicros     int64
}

type CheckResultDetail struct {
	Name   string
	Passed bool
	Detail string
}

func encodeChecks(checks []CheckResultDetail) ([]byte, error) {
	type c struct {
		Name   string `json:"name"`
		Passed bool   `json:"passed"`
		Detail string `json:"detail,omitempty"`
	}
	encoded := make([]c, len(checks))
	for i, ch := range checks {
		encoded[i] = c{ch.Name, ch.Passed, ch.Detail}
	}
	return json.Marshal(encoded)
}

func encodeState(state *State) ([]byte, error) {
	snapshot := map[string]interface{}{
		"system_mode":    state.SystemMode,
		"total_exposure": int64(state.TotalExposure),
		"daily_pnl":     int64(state.DailyPnL),
	}
	return json.Marshal(snapshot)
}

