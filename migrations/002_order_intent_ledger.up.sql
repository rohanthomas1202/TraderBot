-- Phase 3: Order Intent Ledger
-- Append-only ledger for order intents with monotonic versioning.
-- This is the ground truth for exposure accounting.

CREATE TABLE execution.order_intents (
    intent_id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    version             BIGSERIAL NOT NULL UNIQUE,
    trace_id            TEXT NOT NULL,
    approval_hmac       BYTEA NOT NULL,
    strategy_id         TEXT NOT NULL,
    venue               TEXT NOT NULL DEFAULT 'mock',
    market_id           TEXT NOT NULL,
    side                TEXT NOT NULL CHECK (side IN ('buy', 'sell')),
    quantity            INT NOT NULL CHECK (quantity > 0),
    price_micros        BIGINT NOT NULL CHECK (price_micros > 0 AND price_micros < 1000000),
    notional_micros     BIGINT NOT NULL CHECK (notional_micros > 0),
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'open', 'filled', 'cancelled', 'rejected', 'expired')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Idempotency: same trace_id returns existing record
CREATE UNIQUE INDEX idx_intents_trace ON execution.order_intents(trace_id);

-- Fast exposure queries: outstanding intents per market
CREATE INDEX idx_intents_market_status ON execution.order_intents(market_id, status)
    WHERE status IN ('pending', 'open');

-- Strategy-level queries
CREATE INDEX idx_intents_strategy ON execution.order_intents(strategy_id, created_at DESC);

-- Version ordering (for replay)
CREATE INDEX idx_intents_version ON execution.order_intents(version);
