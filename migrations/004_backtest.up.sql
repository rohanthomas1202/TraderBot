-- Backtesting schema: market snapshots and run results

CREATE SCHEMA IF NOT EXISTS backtest;

-- Time-series table for historical market data
CREATE TABLE backtest.market_snapshots (
    id              BIGSERIAL,
    venue           TEXT        NOT NULL,
    market_id       TEXT        NOT NULL,
    captured_at     TIMESTAMPTZ NOT NULL,
    best_bid_micros BIGINT      NOT NULL DEFAULT 0,
    best_ask_micros BIGINT      NOT NULL DEFAULT 0,
    bid_depth       INT         NOT NULL DEFAULT 0,
    ask_depth       INT         NOT NULL DEFAULT 0,
    volume          BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (id, captured_at)
) PARTITION BY RANGE (captured_at);

-- Create monthly partitions for the current and next 3 months
DO $$
DECLARE
    m INT;
    y INT;
    start_date DATE;
    end_date DATE;
BEGIN
    FOR i IN 0..5 LOOP
        start_date := date_trunc('month', CURRENT_DATE + (i || ' month')::interval)::date;
        end_date := date_trunc('month', CURRENT_DATE + ((i+1) || ' month')::interval)::date;
        y := EXTRACT(YEAR FROM start_date);
        m := EXTRACT(MONTH FROM start_date);
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS backtest.market_snapshots_y%sm%s PARTITION OF backtest.market_snapshots FOR VALUES FROM (%L) TO (%L)',
            y, lpad(m::text, 2, '0'), start_date, end_date
        );
    END LOOP;
END $$;

CREATE INDEX idx_snapshots_lookup ON backtest.market_snapshots (venue, market_id, captured_at);

-- Backtest runs
CREATE TABLE backtest.runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    strategy_name   TEXT        NOT NULL,
    venue           TEXT        NOT NULL DEFAULT 'kalshi',
    policy_hash     TEXT        NOT NULL,
    date_from       DATE        NOT NULL,
    date_to         DATE        NOT NULL,
    fill_mode       TEXT        NOT NULL DEFAULT 'deterministic',
    initial_capital BIGINT      NOT NULL DEFAULT 100000000000, -- $100k in micros
    status          TEXT        NOT NULL DEFAULT 'running',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMPTZ
);

-- Backtest results
CREATE TABLE backtest.run_results (
    run_id          UUID PRIMARY KEY REFERENCES backtest.runs(id),
    total_return    DOUBLE PRECISION NOT NULL DEFAULT 0,
    sharpe_ratio    DOUBLE PRECISION,
    max_drawdown    DOUBLE PRECISION NOT NULL DEFAULT 0,
    win_rate        DOUBLE PRECISION NOT NULL DEFAULT 0,
    profit_factor   DOUBLE PRECISION,
    trade_count     INT         NOT NULL DEFAULT 0,
    total_pnl_micros BIGINT     NOT NULL DEFAULT 0,
    trade_log       JSONB       NOT NULL DEFAULT '[]'::jsonb
);
