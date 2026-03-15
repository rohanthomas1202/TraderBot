-- Phase 1 Schema: Paper Trading MVP
-- All trust boundaries preserved. Same schema structure as production.

-- ============================================================
-- Schema: execution (owned by execution-engine)
-- ============================================================
CREATE SCHEMA IF NOT EXISTS execution;

CREATE TABLE execution.orders (
    internal_order_id   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trace_id            UUID NOT NULL,
    strategy_id         TEXT NOT NULL,
    venue               TEXT NOT NULL DEFAULT 'mock',
    market_id           TEXT NOT NULL,
    side                TEXT NOT NULL CHECK (side IN ('buy', 'sell')),
    order_type          TEXT NOT NULL DEFAULT 'limit',
    quantity            INT NOT NULL CHECK (quantity > 0),
    price_micros        BIGINT NOT NULL CHECK (price_micros > 0 AND price_micros < 1000000),
    notional_micros     BIGINT NOT NULL CHECK (notional_micros > 0),

    -- Exchange state
    exchange_order_id   TEXT,
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','open','partially_filled',
                                          'filled','cancelled','rejected','expired','failed')),
    filled_quantity     INT NOT NULL DEFAULT 0 CHECK (filled_quantity >= 0),
    avg_fill_price_micros BIGINT,

    -- Provenance (audit chain)
    credential_id       TEXT NOT NULL DEFAULT 'paper-mode',
    signing_key_id      TEXT,
    approval_hmac       BYTEA NOT NULL,
    policy_config_hash  TEXT NOT NULL,

    -- Timestamps
    proposed_at         TIMESTAMPTZ NOT NULL,
    approved_at         TIMESTAMPTZ NOT NULL,
    submitted_at        TIMESTAMPTZ,
    acknowledged_at     TIMESTAMPTZ,
    last_fill_at        TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Idempotency: prevents duplicate orders from same proposal
    idempotency_key     TEXT UNIQUE NOT NULL
);

CREATE INDEX idx_orders_trace ON execution.orders(trace_id);
CREATE INDEX idx_orders_status ON execution.orders(status)
    WHERE status IN ('pending', 'open', 'partially_filled');
CREATE INDEX idx_orders_strategy ON execution.orders(strategy_id, created_at DESC);
CREATE INDEX idx_orders_venue_market ON execution.orders(venue, market_id);
CREATE INDEX idx_orders_created ON execution.orders(created_at DESC);

CREATE TABLE execution.fills (
    fill_id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    internal_order_id   UUID NOT NULL REFERENCES execution.orders(internal_order_id),
    exchange_fill_id    TEXT,
    quantity            INT NOT NULL CHECK (quantity > 0),
    price_micros        BIGINT NOT NULL CHECK (price_micros > 0),
    fee_micros          BIGINT NOT NULL DEFAULT 0,
    filled_at           TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_fills_order ON execution.fills(internal_order_id);
CREATE INDEX idx_fills_time ON execution.fills(filled_at DESC);


-- ============================================================
-- Schema: risk (owned by risk-engine)
-- ============================================================
CREATE SCHEMA IF NOT EXISTS risk;

CREATE TABLE risk.positions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    venue               TEXT NOT NULL,
    market_id           TEXT NOT NULL,
    strategy_id         TEXT NOT NULL,
    net_quantity        INT NOT NULL DEFAULT 0,
    avg_entry_micros    BIGINT NOT NULL DEFAULT 0,
    notional_micros     BIGINT NOT NULL DEFAULT 0,
    unrealized_pnl_micros BIGINT NOT NULL DEFAULT 0,
    realized_pnl_micros BIGINT NOT NULL DEFAULT 0,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(venue, market_id, strategy_id)
);

CREATE TABLE risk.daily_stats (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    date                DATE NOT NULL,
    scope               TEXT NOT NULL,
    pnl_micros          BIGINT NOT NULL DEFAULT 0,
    turnover_micros     BIGINT NOT NULL DEFAULT 0,
    order_count         INT NOT NULL DEFAULT 0,
    fill_count          INT NOT NULL DEFAULT 0,
    peak_exposure_micros BIGINT NOT NULL DEFAULT 0,
    consecutive_losses  INT NOT NULL DEFAULT 0,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(date, scope)
);

CREATE TABLE risk.policy_decisions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trace_id            UUID NOT NULL,
    strategy_id         TEXT NOT NULL,
    market_id           TEXT NOT NULL,
    decision            TEXT NOT NULL CHECK (decision IN ('approved', 'denied', 'escalated')),
    checks_json         JSONB NOT NULL,
    policy_config_hash  TEXT NOT NULL,
    risk_state_snapshot JSONB NOT NULL,
    decided_at          TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_decisions_trace ON risk.policy_decisions(trace_id);
CREATE INDEX idx_decisions_time ON risk.policy_decisions(decided_at DESC);
CREATE INDEX idx_decisions_strategy ON risk.policy_decisions(strategy_id, decided_at DESC);

CREATE TABLE risk.limits (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    limit_name          TEXT NOT NULL,
    scope               TEXT NOT NULL,
    value_micros        BIGINT NOT NULL,
    unit                TEXT NOT NULL CHECK (unit IN ('usd', 'contracts', 'percent', 'count', 'seconds')),
    active              BOOLEAN NOT NULL DEFAULT TRUE,
    updated_by          TEXT NOT NULL DEFAULT 'system',
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(limit_name, scope)
);

CREATE TABLE risk.limit_history (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    limit_name          TEXT NOT NULL,
    scope               TEXT NOT NULL,
    old_value_micros    BIGINT,
    new_value_micros    BIGINT NOT NULL,
    changed_by          TEXT NOT NULL,
    reason              TEXT,
    changed_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_limit_history_time ON risk.limit_history(changed_at DESC);


-- ============================================================
-- Schema: watchdog (owned by watchdog service)
-- ============================================================
CREATE SCHEMA IF NOT EXISTS watchdog;

CREATE TABLE watchdog.kill_switch_events (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    level               TEXT NOT NULL CHECK (level IN ('soft_pause', 'cancel_only', 'full_stop')),
    scope               TEXT NOT NULL,
    reason              TEXT NOT NULL,
    triggered_by        TEXT NOT NULL,
    acknowledged        BOOLEAN NOT NULL DEFAULT FALSE,
    acknowledged_by     TEXT,
    acknowledged_at     TIMESTAMPTZ,
    root_cause          TEXT,
    resumed             BOOLEAN NOT NULL DEFAULT FALSE,
    resumed_by          TEXT,
    resumed_at          TIMESTAMPTZ,
    triggered_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_kill_active ON watchdog.kill_switch_events(acknowledged, resumed)
    WHERE acknowledged = FALSE OR resumed = FALSE;

CREATE TABLE watchdog.heartbeats (
    service_name        TEXT PRIMARY KEY,
    last_heartbeat_at   TIMESTAMPTZ NOT NULL,
    status              TEXT NOT NULL DEFAULT 'healthy',
    detail              TEXT
);


-- ============================================================
-- Schema: audit (append-only logging)
-- ============================================================
CREATE SCHEMA IF NOT EXISTS audit;

CREATE TABLE audit.event_log (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    timestamp           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    service             TEXT NOT NULL,
    event_type          TEXT NOT NULL,
    trace_id            UUID,
    severity            TEXT NOT NULL DEFAULT 'info' CHECK (severity IN ('info', 'warn', 'critical')),
    payload             JSONB NOT NULL,
    previous_hash       TEXT NOT NULL,
    entry_hash          TEXT NOT NULL
);

CREATE INDEX idx_audit_time ON audit.event_log(timestamp DESC);
CREATE INDEX idx_audit_trace ON audit.event_log(trace_id) WHERE trace_id IS NOT NULL;
CREATE INDEX idx_audit_type ON audit.event_log(event_type, timestamp DESC);
CREATE INDEX idx_audit_severity ON audit.event_log(severity, timestamp DESC)
    WHERE severity IN ('warn', 'critical');

CREATE TABLE audit.operator_actions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    operator_id         TEXT NOT NULL,
    role                TEXT NOT NULL,
    action              TEXT NOT NULL,
    parameters          JSONB NOT NULL DEFAULT '{}',
    performed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE audit.config_changes (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    config_type         TEXT NOT NULL,
    scope               TEXT NOT NULL,
    old_value           JSONB,
    new_value           JSONB NOT NULL,
    changed_by          TEXT NOT NULL,
    reason              TEXT,
    changed_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


-- ============================================================
-- Seed: Default risk limits for paper trading
-- ============================================================
INSERT INTO risk.limits (limit_name, scope, value_micros, unit, updated_by) VALUES
    -- Global limits
    ('max_exposure',         'global',           100000000000, 'usd',       'seed'),  -- $100,000 (paper)
    ('max_daily_loss',       'global',           50000000000,  'usd',       'seed'),  -- $50,000 (paper)
    ('max_drawdown_pct',     'global',           15,           'percent',   'seed'),  -- 15%
    ('trading_hours_start',  'global',           0,            'seconds',   'seed'),  -- 00:00 UTC (24h for paper)
    ('trading_hours_end',    'global',           86400,        'seconds',   'seed'),  -- 24:00 UTC

    -- Per-trade limits
    ('per_trade_notional',   'global',           10000000000,  'usd',       'seed'),  -- $10,000 (paper)
    ('per_trade_max_qty',    'global',           1000,         'contracts', 'seed'),  -- 1000 contracts

    -- Per-strategy limits
    ('strategy_daily_loss',  'strategy:default', 5000000000,   'usd',       'seed'),  -- $5,000 (paper)
    ('strategy_daily_turn',  'strategy:default', 50000000000,  'usd',       'seed'),  -- $50,000 (paper)
    ('strategy_max_orders_per_min', 'strategy:default', 10,    'count',     'seed'),  -- 10/min
    ('strategy_max_consec_losses',  'strategy:default', 10,    'count',     'seed'),  -- 10

    -- Per-venue limits
    ('venue_max_exposure',   'venue:mock',       100000000000, 'usd',       'seed'),  -- $100,000 (paper)
    ('venue_daily_loss',     'venue:mock',       50000000000,  'usd',       'seed'),  -- $50,000 (paper)

    -- Per-market limits
    ('market_max_position',  'global',           5000000000,   'usd',       'seed'),  -- $5,000 per market (paper)
    ('market_max_concentration_pct', 'global',   25,           'percent',   'seed'),  -- 25%

    -- Data quality limits
    ('max_data_age',         'global',           5,            'seconds',   'seed'),  -- 5 seconds
    ('min_orderbook_depth',  'global',           1,            'count',     'seed'),  -- 1 level min
    ('max_spread_bps',       'global',           1000,         'count',     'seed'),  -- 10%

    -- Fat finger
    ('fat_finger_multiple',  'global',           5,            'count',     'seed')   -- 5x avg
ON CONFLICT (limit_name, scope) DO NOTHING;
