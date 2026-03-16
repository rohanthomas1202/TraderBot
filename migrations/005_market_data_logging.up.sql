-- Market data logging for orderbook snapshots and contract resolutions
-- Used by Phase 1 Kalshi market logger to assess liquidity

CREATE SCHEMA IF NOT EXISTS market_data;

CREATE TABLE market_data.orderbook_snapshots (
    id              BIGSERIAL PRIMARY KEY,
    venue           TEXT NOT NULL,
    market_id       TEXT NOT NULL,
    ticker          TEXT NOT NULL,
    title           TEXT,
    category        TEXT,
    bid_price       DOUBLE PRECISION,
    ask_price       DOUBLE PRECISION,
    bid_depth       INTEGER,
    ask_depth       INTEGER,
    mid_price       DOUBLE PRECISION,
    spread          DOUBLE PRECISION,
    volume_24h      DOUBLE PRECISION,
    open_interest   INTEGER,
    expiry          TIMESTAMPTZ,
    captured_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_ob_market_time
    ON market_data.orderbook_snapshots(market_id, captured_at);
CREATE INDEX idx_ob_category_time
    ON market_data.orderbook_snapshots(category, captured_at);

CREATE TABLE market_data.contract_resolutions (
    id              BIGSERIAL PRIMARY KEY,
    venue           TEXT NOT NULL,
    market_id       TEXT NOT NULL,
    ticker          TEXT NOT NULL,
    title           TEXT,
    category        TEXT,
    resolved_yes    BOOLEAN,
    resolved_at     TIMESTAMPTZ,
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_resolutions_market
    ON market_data.contract_resolutions(market_id, resolved_at);
CREATE INDEX idx_resolutions_category
    ON market_data.contract_resolutions(category, resolved_at);
