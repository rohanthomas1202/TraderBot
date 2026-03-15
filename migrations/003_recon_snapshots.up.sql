-- Phase 8: Reconciliation snapshots
CREATE SCHEMA IF NOT EXISTS recon;

CREATE TABLE recon.snapshots (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    venue               TEXT NOT NULL,
    snapshot_type       TEXT NOT NULL CHECK (snapshot_type IN ('positions', 'orders', 'balance')),
    matches             BOOLEAN NOT NULL,
    internal_state      JSONB NOT NULL,
    exchange_state      JSONB NOT NULL,
    mismatches          JSONB,  -- null if matches=true
    severity            TEXT NOT NULL DEFAULT 'info' CHECK (severity IN ('info', 'warn', 'critical')),
    captured_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_recon_time ON recon.snapshots(captured_at DESC);
CREATE INDEX idx_recon_mismatch ON recon.snapshots(matches, captured_at DESC) WHERE matches = FALSE;
