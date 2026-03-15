# Autonomous Trading Platform — Phased Build Plan

## Design Principles

1. **Kalshi-first.** The initial rollout targets Kalshi as the sole venue. All adapters, market mappings, and API clients are built for Kalshi. Polymarket is a later addition (Phase 13) and is never a prerequisite for earlier work.
2. **Paper mode before real money.** Every phase through Phase 10 runs against mock or shadow data. No real orders are placed until Phase 11.
3. **Small-team safety.** Each phase is scoped so a single engineer (or a pair) can build, test, and ship it in isolation. No phase requires more than one service to be running unless the phase explicitly wires services together.
4. **Dependencies are strict.** A phase may only test components built in that phase or earlier phases. Tests must never reference code that doesn't exist yet.

## How to Use This Document

Each phase is self-contained. At the end of each phase:
1. **STOP** and verify the exit criteria
2. Run every test in the "Tests" section
3. Fix any failures before proceeding
4. Commit using the provided commit checkpoint
5. **Wait for explicit approval** before starting the next phase

Do NOT proceed to the next phase until the current phase is fully tested and committed.

---

## Phase 1: Foundation — Infrastructure, Models, and Audit Logging

### Goal
Standing infrastructure, shared types, database schema, audit logging, and event bus. No services run yet. The goal is to prove that the data layer is solid.

### Dependencies
None — this is the base layer.

### Build Items
- PostgreSQL database with all schemas (execution, risk, watchdog, audit)
- NATS JetStream with stream configuration
- `internal/models/` — Money, Order, Market types with validation
- `internal/audit/` — hash-chained audit logger writing to PostgreSQL
- `internal/events/` — NATS publisher/subscriber with stream auto-creation
- `internal/config/` — policy loader and validator
- `migrations/001_initial.up.sql` and `001_initial.down.sql`
- `policies/paper.yaml`
- `configs/local.dev.yaml`
- `.env.example`
- `docker-compose.yml` (postgres + nats only)
- `Makefile` targets: `up`, `down`, `migrate`, `migrate-down`, `reset-db`

### Tests

```bash
# 1. Start infrastructure
cd ~/TraderBot
make up

# 2. Verify PostgreSQL is healthy
docker compose exec postgres pg_isready -U trader -d autonomy
# Expected: "accepting connections"

# 3. Run migrations
make migrate

# 4. Verify schemas exist
docker compose exec postgres psql -U trader -d autonomy -c "\dn"
# Expected: execution, risk, watchdog, audit schemas listed

# 5. Verify tables exist
docker compose exec postgres psql -U trader -d autonomy -c "\dt execution.*"
docker compose exec postgres psql -U trader -d autonomy -c "\dt risk.*"
docker compose exec postgres psql -U trader -d autonomy -c "\dt watchdog.*"
docker compose exec postgres psql -U trader -d autonomy -c "\dt audit.*"
# Expected: orders, fills, positions, daily_stats, policy_decisions, limits,
#           limit_history, kill_switch_events, heartbeats, event_log,
#           operator_actions, config_changes

# 6. Verify seeded limits
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT limit_name, scope, value_micros, unit FROM risk.limits ORDER BY scope, limit_name;"
# Expected: ~18 rows of paper-trading limits

# 7. Verify NATS is healthy
curl -s http://localhost:8222/healthz
# Expected: {"status":"ok"}

# 8. Verify NATS JetStream is enabled
curl -s http://localhost:8222/jsz
# Expected: JSON with jetstream config info

# 9. Test migration rollback and re-apply
make migrate-down
make migrate
# Expected: no errors, schemas recreated

# 10. Test Go compilation of shared packages
cd ~/TraderBot
go build ./internal/models/...
go build ./internal/config/...
# Expected: no errors (audit and events need dependencies wired)

# 11. Verify policy file loads
cat policies/paper.yaml | head -5
# Expected: "mode: paper" on first non-comment line
```

### Exit Criteria
- Docker Compose brings up Postgres and NATS cleanly
- All four DB schemas created with expected tables
- Migration up/down/up is idempotent
- Shared Go packages compile without errors
- Policy YAML parses and validates

### Commit Checkpoint
```
feat: Phase 1 — foundation infrastructure, models, and audit logging

- Docker Compose with PostgreSQL 16 and NATS JetStream
- Database schemas: execution, risk, watchdog, audit with migrations
- Seeded paper-trading risk limits
- Domain models: Money (microdollar), Order lifecycle state machine, MarketData
- Hash-chained audit logger (audit.event_log)
- NATS JetStream publisher with auto-created streams
- Policy loader with validation and deterministic config hash
- Event schemas for full order lifecycle

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
```

---

## Phase 2: Risk Engine — Policy Evaluation and HMAC Approvals

### Goal
A working risk engine that loads state from the database, evaluates proposed orders against 20 pre-trade checks, and produces HMAC-signed approvals or denials. This is the core safety boundary of the platform. Tested in-process only — no gRPC, no execution engine.

### Dependencies
- Phase 1 (database, models, audit, events, config)

### Build Items
- `services/risk/engine.go` — main engine with state management
- `services/risk/checks.go` — all 20 pre-trade check implementations
- `cmd/risk-engine/main.go` — service entrypoint with DB/NATS init
- Wire risk engine to audit logger and event publisher
- HMAC approval signing and verification functions
- Risk state loading from PostgreSQL on startup
- Fill reporting that updates positions and daily stats

### Tests

```bash
# Prerequisites: Phase 1 complete, infrastructure running

# 1. Compile risk engine
go build ./cmd/risk-engine/
go build ./services/risk/...
# Expected: no errors

# 2. Run risk engine (it will load state from DB and wait)
go run ./cmd/risk-engine &
RISK_PID=$!
sleep 2
# Expected: log output showing "policy loaded", "risk state loaded", "risk engine ready"

# 3. Kill it cleanly
kill $RISK_PID
wait $RISK_PID 2>/dev/null

# 4. Run unit tests for risk checks (in-process, no external deps beyond DB)
go test ./services/risk/... -v -count=1
# Expected: all check logic tests pass

# 5. Run integration tests for denial scenarios
make up && make migrate
go test ./tests/integration/denied_order_test.go \
        ./tests/integration/duplicate_order_test.go \
        -tags=integration -v -count=1 -run "TestDenied|TestDuplicate"
# Expected: all denial tests pass
# - TestDeniedOrder_ExceedsNotionalLimit: denied by per_trade_notional
# - TestDeniedOrder_MarketNotAllowed: denied by market_allowed
# - TestDeniedOrder_StaleData: denied by data_freshness
# - TestDuplicateOrder_SecondProposalDenied: denied by duplicate_order

# 6. Verify decisions are persisted
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT trace_id, decision, policy_config_hash FROM risk.policy_decisions ORDER BY decided_at DESC LIMIT 10;"
# Expected: rows showing 'approved' and 'denied' decisions with policy hash

# 7. Verify HMAC signing produces verifiable approvals
go test ./services/risk/... -v -count=1 -run "TestHMAC"
# Expected: sign-then-verify round-trip passes; tampered payload fails verification

# 8. Verify audit entries for risk decisions
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT event_type, severity, trace_id FROM audit.event_log WHERE event_type LIKE 'order.%' ORDER BY timestamp DESC LIMIT 10;"
# Expected: order.approved and order.denied entries
```

### Exit Criteria
- Risk engine starts, loads policy and state from DB, and shuts down cleanly
- All 20 checks produce correct approve/deny decisions for known inputs
- HMAC signatures verify correctly; tampered payloads are rejected
- Decisions are persisted to `risk.policy_decisions` with full check results
- Audit log contains entries for every decision

### Commit Checkpoint
```
feat: Phase 2 — risk engine with 20 pre-trade checks and HMAC approvals

- Risk engine loads state from PostgreSQL on startup
- 20 pre-trade checks: market allowlist, size limits, position limits,
  concentration, drawdown, data freshness, duplicate detection, fat finger
- All checks run without short-circuit for complete audit trail
- HMAC-SHA256 signed approvals verified independently by downstream services
- Policy decisions persisted with full check results and risk state snapshot
- Fill reporting updates positions and daily stats in real time
- Integration tests: denied orders, duplicates, stale data, market allowlist

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
```

---

## Phase 3: Order Intent Ledger — Monotonic Versioning and Exposure Invariants

### Goal
An append-only order intent ledger that records every proposed order with a monotonically increasing version number before anything is sent to an exchange. This is the ground truth for exposure accounting. The ledger enforces invariants: no intent can exceed pre-approved limits, and the exposure state is always reconstructable from the ledger alone.

### Dependencies
- Phase 1 (database, models, audit)
- Phase 2 (risk engine, HMAC approvals — intents reference signed approvals)

### Build Items
- `internal/ledger/intent.go` — OrderIntent struct with monotonic version assignment
- `internal/ledger/ledger.go` — append-only ledger backed by PostgreSQL
- `internal/ledger/exposure.go` — exposure state derived from ledger replay
- `migrations/002_order_intent_ledger.up.sql` / `002_order_intent_ledger.down.sql`
  - Table: `execution.order_intents` (intent_id, version BIGSERIAL, trace_id, approval_hmac, side, quantity, price_micros, market_id, strategy_id, status, created_at)
  - Unique constraint on version (monotonic, gapless via SERIAL)
  - Index on (market_id, status) for fast exposure queries
- Exposure calculation: sum of outstanding (pending + open) intent quantities per market, per side
- Exposure invariant check: before writing a new intent, verify that adding it does not violate the approved exposure envelope from the risk engine
- Ledger replay: reconstruct full exposure state from `SELECT * FROM execution.order_intents ORDER BY version`
- Idempotency: duplicate intent (same trace_id) returns the existing record, not a new one

### Tests

```bash
# Prerequisites: Phase 1 + 2 complete, infrastructure running

# 1. Run migration
make migrate
# Expected: execution.order_intents table created

# 2. Compile ledger package
go build ./internal/ledger/...
# Expected: no errors

# 3. Unit tests — monotonic versioning
go test ./internal/ledger/... -v -count=1 -run "TestMonotonic"
# Expected: versions are sequential, no gaps, concurrent writes serialize correctly

# 4. Unit tests — exposure calculation
go test ./internal/ledger/... -v -count=1 -run "TestExposure"
# Expected:
# - Empty ledger → zero exposure
# - One buy intent → exposure equals intent quantity
# - Filled intent removed from outstanding exposure
# - Cancelled intent removed from outstanding exposure

# 5. Unit tests — exposure invariant enforcement
go test ./internal/ledger/... -v -count=1 -run "TestExposureInvariant"
# Expected:
# - Intent within limits → accepted
# - Intent that would exceed position limit → rejected before write
# - Intent that would exceed per-market exposure → rejected before write

# 6. Unit tests — idempotency
go test ./internal/ledger/... -v -count=1 -run "TestIdempotency"
# Expected: same trace_id submitted twice → same intent_id returned, version unchanged

# 7. Integration test — ledger replay reconstructs state
go test ./tests/integration/ledger_test.go -tags=integration -v -count=1 -run "TestLedgerReplay"
# Expected: write 100 intents, mark some filled/cancelled, replay from DB,
#           replayed exposure matches live-computed exposure exactly

# 8. Verify ledger contents in DB
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT version, trace_id, side, quantity, market_id, status
      FROM execution.order_intents ORDER BY version LIMIT 10;"
# Expected: sequential versions, correct fields
```

### Exit Criteria
- Versions are monotonically increasing and gapless under concurrent access
- Exposure state is always reconstructable from a full ledger replay
- No intent can be written that would violate the approved exposure envelope
- Duplicate trace_ids are handled idempotently
- All intent lifecycle transitions (pending → open → filled/cancelled) update exposure correctly

### Commit Checkpoint
```
feat: Phase 3 — order intent ledger with monotonic versioning and exposure invariants

- Append-only order intent ledger backed by PostgreSQL
- Monotonic BIGSERIAL versioning with gapless guarantee
- Exposure state derived from outstanding intents per market/side
- Pre-write invariant check: new intent cannot exceed approved exposure envelope
- Idempotent writes keyed on trace_id
- Ledger replay reconstructs identical exposure state from DB
- Migration 002: execution.order_intents table with indexes

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
```

---

## Phase 4: Execution Engine — Paper-Mode Order Lifecycle

### Goal
A working execution engine that receives HMAC-approved orders, records them in the intent ledger, submits them to the mock exchange, tracks order state through the full lifecycle, polls for fills, and persists everything to PostgreSQL.

### Dependencies
- Phase 1 (database, models, audit, events)
- Phase 2 (risk engine, HMAC verification)
- Phase 3 (order intent ledger — every submission must have a recorded intent)

### Build Items
- `services/execution/engine.go` — order submission, lifecycle, fill polling
- `services/execution/paper_adapter.go` — mock venue adapter (in-process)
- `services/mockexchange/server.go` — full mock exchange with configurable behavior
- `cmd/execution-engine/main.go` — service entrypoint
- Order state machine with validated transitions
- Fill processing and risk engine notification
- Integration with intent ledger: every order submission creates/updates an intent

### Tests

```bash
# Prerequisites: Phase 1 + 2 + 3 complete, infrastructure running

# 1. Compile execution engine and mock exchange
go build ./cmd/execution-engine/
go build ./services/execution/...
go build ./services/mockexchange/...
# Expected: no errors

# 2. Run the order lifecycle integration test
go test ./tests/integration/order_lifecycle_test.go -tags=integration -v -count=1
# Expected: TestOrderLifecycle_ProposalToFill passes
# Output shows: propose → approve → record intent → submit → fill → state updated

# 3. Verify order persistence
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT internal_order_id, status, filled_quantity, strategy_id, market_id
      FROM execution.orders ORDER BY created_at DESC LIMIT 10;"
# Expected: orders with various statuses (filled, cancelled, etc.)

# 4. Verify fill persistence
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT f.fill_id, f.quantity, f.price_micros, o.market_id
      FROM execution.fills f JOIN execution.orders o ON f.internal_order_id = o.internal_order_id
      ORDER BY f.filled_at DESC LIMIT 10;"
# Expected: fill records linked to orders

# 5. Verify intent ledger was updated by execution engine
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT version, trace_id, status FROM execution.order_intents ORDER BY version DESC LIMIT 10;"
# Expected: intents with status matching order outcomes (filled, cancelled)

# 6. Verify idempotency (duplicate rejection)
go test ./tests/integration/order_lifecycle_test.go -tags=integration -v -count=1 -run "TestDuplicateSubmission"
# Expected: second identical order rejected by ledger idempotency

# 7. Verify order state machine rejects illegal transitions
go test ./services/execution/... -v -count=1
# Expected: all unit tests pass

# 8. Run execution engine as a service
go run ./cmd/execution-engine &
EXEC_PID=$!
sleep 2
# Expected: "loaded open orders", "execution engine ready"
kill $EXEC_PID

# 9. Verify audit trail for submitted orders
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT event_type, trace_id, severity FROM audit.event_log
      WHERE event_type LIKE 'order.submitted%' OR event_type LIKE 'order.rejected%'
      ORDER BY timestamp DESC LIMIT 10;"
# Expected: submitted and/or rejected entries
```

### Exit Criteria
- Execution engine only accepts orders with valid HMAC approvals
- Every submission is recorded in the intent ledger before reaching the mock exchange
- Order state machine enforces valid transitions; illegal transitions are logged
- Fills are persisted and the risk engine is notified
- Duplicate submissions are rejected idempotently
- Mock exchange is configurable (fill delay, probability, partials, rejections)

### Commit Checkpoint
```
feat: Phase 4 — execution engine with paper-mode order lifecycle

- Execution engine validates HMAC approvals before submitting orders
- All orders recorded in intent ledger before submission
- Order state machine with validated transitions and illegal transition detection
- Mock exchange: configurable fill delay, probability, partial fills, rejections
- Fill polling loop with risk engine notification
- Full order persistence: orders table + fills table with referential integrity
- Intent ledger updated on fill/cancel for accurate exposure tracking
- All events published to NATS and audit-logged

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
```

---

## Phase 5: Watchdog — Kill Switches, Dead Man's Switch, Health Monitoring

### Goal
A watchdog service that monitors system health, manages kill switches with persistence across restarts, enforces dead man's switch heartbeats, and provides the safety net that makes autonomous operation possible.

### Dependencies
- Phase 1 (database, audit, events)
- Phase 4 (execution engine — watchdog calls CancelAll on kill switch)

### Build Items
- `services/watchdog/killswitch.go` — kill switch manager with DB persistence
- `services/watchdog/heartbeat.go` — dead man's switch monitor
- `cmd/watchdog/main.go` — service entrypoint
- `cmd/trade-ctl/main.go` — operator CLI with `kill`, `ack`, `resume`, `status` commands
- Kill switch levels: soft_pause, cancel_only, full_stop
- Acknowledge → resume lifecycle with mandatory root-cause documentation
- Halt persistence across restarts (loaded from DB on startup)
- Dead man's switch: if critical services miss heartbeats, auto-trigger kill switch
- Integration with execution engine (CancelAll on kill switch)
- Integration with risk engine (SetSystemMode on kill switch)

### Tests

```bash
# Prerequisites: Phase 1 + 2 + 3 + 4 complete, infrastructure running

# 1. Compile watchdog and trade-ctl
go build ./cmd/watchdog/
go build ./cmd/trade-ctl/
go build ./services/watchdog/...
# Expected: no errors

# 2. Run kill switch unit tests
go test ./services/watchdog/... -v -count=1
# Expected: all pass — severity hierarchy, scope logic, ack-before-resume

# 3. Run kill switch integration tests
go test ./tests/integration/kill_switch_test.go -tags=integration -v -count=1
# Expected: all three tests pass:
# - TestKillSwitch_BlocksNewOrders: orders rejected during kill switch
# - TestKillSwitch_AckResumeFlow: full ack→resume lifecycle works
# - TestKillSwitch_PersistsSurvivesRestart: halts survive process restart

# 4. Test via CLI — trigger kill switch
go run ./cmd/trade-ctl status
# Expected: "Mode: NORMAL"

go run ./cmd/trade-ctl kill --level cancel_only --scope global --reason "Phase 5 testing"
go run ./cmd/trade-ctl status
# Expected: HALT shown with level=cancel_only

# 5. Test resume without ack — should fail
go run ./cmd/trade-ctl resume --scope global
# Expected: error "halt must be acknowledged before resuming"

# 6. Acknowledge the halt
go run ./cmd/trade-ctl ack --scope global --cause "Phase 5 test, no real issue"
go run ./cmd/trade-ctl status
# Expected: halt shows as acknowledged

# 7. Resume trading
go run ./cmd/trade-ctl resume --scope global
go run ./cmd/trade-ctl status
# Expected: "Mode: NORMAL"

# 8. Verify kill switch events in database
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT level, scope, reason, triggered_by, acknowledged, resumed
      FROM watchdog.kill_switch_events ORDER BY triggered_at DESC LIMIT 5;"
# Expected: events showing the full trigger→ack→resume cycle

# 9. Verify audit trail for kill switch
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT event_type, severity, payload->>'level' as level, payload->>'scope' as scope
      FROM audit.event_log WHERE event_type LIKE 'kill_switch%'
      ORDER BY timestamp DESC LIMIT 5;"
# Expected: kill_switch.activated entries with severity=critical

# 10. Test dead man's switch (manual)
HEARTBEAT_INTERVAL_SEC=3 HEARTBEAT_GRACE_MULTIPLE=2 go run ./cmd/watchdog &
WD_PID=$!
sleep 10
# Expected: after ~9 seconds, watchdog triggers cancel_only
# because no execution-engine or risk-engine sent heartbeats
go run ./cmd/trade-ctl status
# Expected: HALT active with reason containing "heartbeat missed"
kill $WD_PID
# Cleanup: ack and resume the halt
go run ./cmd/trade-ctl ack --scope global --cause "dead man switch test"
go run ./cmd/trade-ctl resume --scope global
```

### Exit Criteria
- Kill switch activates and persists across process restarts
- Cannot resume without acknowledging with a root cause
- Severity hierarchy prevents downgrading a halt
- Dead man's switch fires when heartbeats are missed
- `trade-ctl` CLI can trigger, ack, resume, and query status
- All kill switch events are audit-logged at critical severity

### Commit Checkpoint
```
feat: Phase 5 — watchdog with kill switches, dead man's switch, and health monitoring

- Kill switch manager: soft_pause, cancel_only, full_stop levels
- Scoped kill switches: global, per-venue, per-strategy
- Mandatory acknowledge-before-resume with root cause documentation
- Halt persistence across process restarts (loaded from DB on startup)
- Dead man's switch: auto-triggers cancel_only if critical services miss heartbeats
- trade-ctl CLI: kill, ack, resume, status operator commands
- All kill switch events audit-logged at critical severity
- Integration tests: blocks orders, ack/resume flow, survives restart

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
```

---

## Phase 6: Data Ingestion and Strategy Engine — Signal Generation Pipeline

### Goal
Market data flowing through the system and a simple strategy producing order proposals that are evaluated by the risk engine. Tested with in-process wiring (direct function calls). The full gRPC pipeline is deferred to Phase 7.

### Dependencies
- Phase 1 (models, events, config)
- Phase 2 (risk engine — strategy proposes orders for evaluation)

### Build Items
- `services/data/ingestion.go` — mock market data feed publishing to NATS
- `services/strategy/engine.go` — signal loop consuming data, proposing orders
- `services/strategy/simple_momentum.go` — test strategy (buy < 40¢, sell > 60¢)
- `cmd/data-ingestion/main.go` — service entrypoint
- `cmd/strategy-engine/main.go` — service entrypoint
- Market data subscription in risk engine (updates freshness cache)

### Tests

```bash
# Prerequisites: Phase 1 + 2 complete, infrastructure running

# 1. Compile
go build ./cmd/data-ingestion/
go build ./cmd/strategy-engine/
go build ./services/data/...
go build ./services/strategy/...
# Expected: no errors

# 2. Run data ingestion and verify market data flows
go run ./cmd/data-ingestion &
DATA_PID=$!
sleep 3

# Check NATS has messages on the DATA stream
curl -s http://localhost:8222/jsz | python3 -m json.tool | grep -A5 DATA
# Expected: DATA stream with messages > 0

kill $DATA_PID

# 3. Run strategy engine with stub evaluator and verify it generates signals
go run ./cmd/data-ingestion &
DATA_PID=$!
sleep 1
go run ./cmd/strategy-engine &
STRAT_PID=$!
sleep 10
# Expected: log output showing:
# - "data ingestion ready"
# - "strategy engine ready"
# - "stub evaluator: would send to risk engine" for each signal generated
kill $DATA_PID $STRAT_PID 2>/dev/null

# 4. Run data ingestion + risk engine together (in-process data subscription)
go run ./cmd/data-ingestion &
DATA_PID=$!
sleep 2
go run ./cmd/risk-engine &
RISK_PID=$!
sleep 3
# Expected: risk engine shows "risk engine ready" and receives market data updates
kill $DATA_PID $RISK_PID 2>/dev/null

# 5. Verify NATS streams have data
curl -s http://localhost:8222/jsz | python3 -m json.tool
# Expected: DATA stream with message counts > 0

# 6. Unit tests for strategy signal generation
go test ./services/strategy/... -v -count=1
# Expected: all pass — buy below 40¢, sell above 60¢, no signal in dead zone
```

### Exit Criteria
- Mock data feed publishes market data to NATS every 1 second for 5 simulated markets
- Strategy engine generates correct buy/sell signals based on price thresholds
- Risk engine subscribes to market data and updates its freshness cache
- Strategy engine works with a stub evaluator (no gRPC required)
- All unit tests pass

### Commit Checkpoint
```
feat: Phase 6 — data ingestion and strategy engine signal pipeline

- Mock market data feed: 5 simulated markets with random-walk prices
- Market data published to NATS every 1 second
- Simple momentum strategy: buy below 40¢, sell above 60¢
- Strategy engine signal loop with configurable interval
- Risk engine subscribes to market data for freshness cache updates
- Order proposal events published to NATS

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
```

---

## Phase 7: gRPC Wiring — Connect All Services

### Goal
Wire all services together via gRPC so the full pipeline runs across processes: data-ingestion → strategy-engine → risk-engine → execution-engine → mock-exchange. This is where the architecture becomes a running system.

### Dependencies
- Phase 1–6 (all services exist and are individually tested)

### Build Items
- Protobuf definitions and code generation (`protoc` or `buf`)
- gRPC server in `risk-engine` exposing EvaluateOrder, GetRiskState, ReportFill, UpdateLimit
- gRPC server in `execution-engine` exposing SubmitOrder, CancelOrder, CancelAll, GetOrders
- gRPC server in `watchdog` exposing TriggerKillSwitch, GetSystemStatus, Heartbeat, Acknowledge, Resume
- gRPC client in `strategy-engine` connecting to risk-engine
- gRPC client in `execution-engine` connecting to watchdog (heartbeat)
- gRPC client in `watchdog` connecting to execution-engine (CancelAll on kill switch)
- Service caller validation using `AllowedCallers` map
- Heartbeat loop in execution-engine reporting to watchdog
- Fill polling loop in execution-engine with risk engine callback

### Tests

```bash
# Prerequisites: Phase 1-6 complete, infrastructure running

# 1. Generate protobuf code
make proto
# Expected: gen/ directory with Go files for all proto definitions

# 2. Compile all services
go build ./cmd/...
# Expected: no errors

# 3. Run the full system (5 terminals or use make simulate)

# Terminal 1:
go run ./cmd/watchdog

# Terminal 2:
go run ./cmd/data-ingestion

# Terminal 3:
go run ./cmd/risk-engine

# Terminal 4:
go run ./cmd/execution-engine

# Terminal 5:
go run ./cmd/strategy-engine

# 4. Wait 30 seconds and observe logs
# Expected flow visible in logs:
# data-ingestion: publishing market data every 1s
# strategy-engine: generating signals, calling risk-engine via gRPC
# risk-engine: evaluating orders, approving/denying
# execution-engine: receiving approved orders, submitting to mock exchange
# execution-engine: polling fills, reporting to risk-engine
# watchdog: receiving heartbeats from execution-engine

# 5. Check orders were created
go run ./cmd/trade-ctl orders
# Expected: recent orders with various statuses

# 6. Check risk state
go run ./cmd/trade-ctl risk
# Expected: daily stats with P&L, turnover, order/fill counts

# 7. Check system status
go run ./cmd/trade-ctl status
# Expected: NORMAL mode, open order count, daily P&L

# 8. Test kill switch across services
go run ./cmd/trade-ctl kill --level cancel_only --scope global --reason "cross-service test"
# Expected: watchdog calls execution-engine CancelAll, all orders cancelled
go run ./cmd/trade-ctl status
# Expected: HALT active, 0 open orders

# New orders should be blocked:
# strategy-engine logs should show orders being denied (system_mode check)

# Resume:
go run ./cmd/trade-ctl ack --scope global --cause "cross-service test complete"
go run ./cmd/trade-ctl resume --scope global
# Expected: trading resumes, new orders start flowing

# 9. Test dead man's switch across services
# Kill the execution engine
pkill -f "go run ./cmd/execution-engine"
# Wait for heartbeat timeout
sleep 35
go run ./cmd/trade-ctl status
# Expected: HALT active with "heartbeat missed" reason

# Restart execution engine and resume
go run ./cmd/execution-engine &
go run ./cmd/trade-ctl ack --scope global --cause "execution engine restarted"
go run ./cmd/trade-ctl resume --scope global

# 10. Run all integration tests
go test ./tests/integration/... -tags=integration -v -count=1
# Expected: all tests pass

# 11. Cleanup
pkill -f "go run ./cmd/" 2>/dev/null || true
```

### Exit Criteria
- All five services start and communicate via gRPC
- Full pipeline produces orders from strategy signals through to mock fills
- Kill switch propagates across services (watchdog → execution CancelAll)
- Dead man's switch fires when execution engine is killed
- Heartbeats flow from execution engine to watchdog
- All integration tests pass

### Commit Checkpoint
```
feat: Phase 7 — gRPC wiring connecting all services end-to-end

- gRPC servers: risk-engine, execution-engine, watchdog
- gRPC clients: strategy→risk, execution→watchdog, watchdog→execution
- Protobuf code generation with buf/protoc
- Service identity validation on all endpoints
- Heartbeat loop: execution-engine → watchdog (10s interval)
- Fill polling loop: execution-engine polls mock exchange (2s interval)
- Kill switch propagation across services via gRPC
- Dead man's switch fires across process boundaries
- Full pipeline: data → strategy → risk → execution → mock exchange → fill → risk

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
```

---

## Phase 8: Reconciliation and Recovery

### Goal
Shadow accounting that compares internal state against the mock exchange, crash recovery that rebuilds state from PostgreSQL, and data integrity verification. The intent ledger from Phase 3 is the authoritative source for exposure replay during recovery.

### Dependencies
- Phase 1–7 (full running system required for reconciliation)
- Phase 3 (intent ledger — recovery replays ledger to rebuild exposure state)

### Build Items
- `services/recon/engine.go` — reconciliation engine
- `services/recon/comparator.go` — internal vs exchange state comparison
- `cmd/reconciliation/main.go` — service entrypoint (or integrate into watchdog)
- Periodic reconciliation: every 60 seconds, compare positions, open orders, balance
- Mismatch detection with configurable thresholds
- Auto-trigger kill switch on mismatch exceeding threshold
- Reconciliation results persisted to `recon.snapshots`
- Crash recovery: execution engine replays intent ledger, risk engine reloads state
- Startup reconciliation: on restart, verify internal state matches exchange before trading

### Tests

```bash
# Prerequisites: Phase 1-7 complete, full system running

# 1. Compile
go build ./services/recon/...
# Expected: no errors

# 2. Run recovery integration test
go test ./tests/integration/recovery_test.go -tags=integration -v -count=1
# Expected: both tests pass
# - TestRecovery_OpenOrdersSurviveRestart: orders reloaded from DB + intent ledger
# - TestRecovery_RiskStateSurvivesRestart: positions and P&L restored

# 3. Test intent ledger replay during recovery
go test ./tests/integration/recovery_test.go -tags=integration -v -count=1 -run "TestRecovery_LedgerReplay"
# Expected: exposure state after replay matches pre-crash exposure exactly

# 4. Test reconciliation with running system
# Start all services, let them run for 30 seconds
make simulate &
sleep 30

# Check reconciliation results
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT venue, snapshot_type, matches, captured_at
      FROM recon.snapshots ORDER BY captured_at DESC LIMIT 10;"
# Expected: rows showing position/order/balance checks, all matching

# 5. Simulate crash recovery
# Kill execution engine abruptly
pkill -f "go run ./cmd/execution-engine"
sleep 2

# Restart it
go run ./cmd/execution-engine &
sleep 3
# Expected: logs show "replayed intent ledger", "loaded X open orders" from DB

# 6. Verify no orphaned orders
go run ./cmd/trade-ctl orders
# Expected: orders recovered, statuses consistent

# 7. Check audit log for reconciliation events
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT event_type, severity, payload->>'matches' as matches
      FROM audit.event_log WHERE event_type LIKE 'recon%'
      ORDER BY timestamp DESC LIMIT 5;"

# 8. Cleanup
pkill -f "go run ./cmd/" 2>/dev/null || true
```

### Exit Criteria
- Reconciliation runs every 60 seconds with no false mismatches during normal operation
- Critical mismatches auto-trigger kill switch
- Execution engine recovers open orders from DB and replays intent ledger on restart
- Risk engine recovers positions, P&L, and active halts from DB on restart
- Startup reconciliation gate prevents trading until state is verified
- Reconciliation results are persisted to `recon.snapshots`

### Commit Checkpoint
```
feat: Phase 8 — reconciliation engine and crash recovery

- Shadow accounting: periodic comparison of internal vs exchange state
- Position, order count, and balance reconciliation every 60 seconds
- Mismatch detection with configurable thresholds
- Auto kill switch on critical mismatches
- Reconciliation results persisted to recon.snapshots
- Execution engine crash recovery: replays intent ledger + reloads open orders
- Risk engine crash recovery: restores positions, P&L, and active halts
- Startup reconciliation gate: must pass before trading enabled

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
```

---

## Phase 9: Operator Dashboard (CLI) and Observability

### Goal
Complete the `trade-ctl` CLI with all operator commands, add structured logging across all services, and provide enough observability to operate the system confidently during paper trading.

### Dependencies
- Phase 1–8 (all services and reconciliation must exist for full CLI coverage)

### Build Items
- Complete `cmd/trade-ctl/main.go` with all commands
- Add `trade-ctl audit` — query audit log with filters
- Add `trade-ctl trace <trace_id>` — full trace of a single order's lifecycle (including intent ledger entry)
- Add `trade-ctl config` — show running config for all services
- Add `trade-ctl ledger` — query intent ledger, show exposure by market
- Structured JSON logging (slog) across all services with consistent fields
- Log correlation via trace_id in all log output
- Health check endpoints for each service

### Tests

```bash
# Prerequisites: Phase 1-8 complete, infrastructure running

# 1. Compile trade-ctl
go build ./cmd/trade-ctl/
# Expected: no errors

# 2. Run all services and generate some traffic
make simulate &
sleep 30

# 3. Test every CLI command
go run ./cmd/trade-ctl status
go run ./cmd/trade-ctl orders
go run ./cmd/trade-ctl risk
go run ./cmd/trade-ctl limits
go run ./cmd/trade-ctl policy
go run ./cmd/trade-ctl ledger
# Expected: each command produces readable output with real data

# 4. Test audit query
go run ./cmd/trade-ctl audit --type "order.approved" --limit 5
go run ./cmd/trade-ctl audit --severity critical --limit 5
# Expected: filtered audit entries

# 5. Test trace command
TRACE_ID=$(docker compose exec -T postgres psql -U trader -d autonomy -t \
  -c "SELECT trace_id FROM execution.orders ORDER BY created_at DESC LIMIT 1;" | tr -d ' ')
go run ./cmd/trade-ctl trace $TRACE_ID
# Expected: full lifecycle showing proposal, intent version, risk decision, submission, fills

# 6. Test health checks
go run ./cmd/trade-ctl status
# Expected: service health section shows all services healthy

# 7. Verify structured logging
go run ./cmd/risk-engine 2>&1 | head -5
# Expected: JSON log lines with service, level, msg, and trace_id fields

# 8. Cleanup
pkill -f "go run ./cmd/" 2>/dev/null || true
```

### Exit Criteria
- All CLI commands produce correct output with real data
- `trace` command shows the full lifecycle including intent ledger version
- `ledger` command shows current exposure by market
- Structured JSON logging is consistent across all services
- Trace IDs propagate through every log line
- Health check endpoints report service status

### Commit Checkpoint
```
feat: Phase 9 — operator CLI, structured logging, and observability

- Complete trade-ctl CLI: status, kill, ack, resume, orders, risk, limits,
  policy, audit, trace, config, ledger commands
- Audit log query with event type, severity, and time range filters
- Trade tracing: full lifecycle reconstruction including intent ledger version
- Ledger command: current exposure summary by market
- Structured JSON logging (slog) across all services
- Trace ID propagation in every log line for correlation
- Health check gRPC endpoints on all services

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
```

---

## Phase 10: Full Integration Testing and Paper Trading Certification

### Goal
Run the complete system for an extended period, verify all safety mechanisms work, and certify the platform for Phase 11 (real Kalshi data, still no real money).

### Dependencies
- Phase 1–9 (complete system with observability)

### Build Items
- Extended integration test suite covering all failure scenarios
- Simulation harness that runs for configurable duration
- Automated test scenarios:
  - Normal trading flow
  - Kill switch activation and recovery
  - Dead man's switch activation
  - Stale data handling
  - Duplicate order rejection
  - Fat finger detection
  - Position limit enforcement
  - Daily loss limit enforcement
  - Crash recovery with intent ledger replay
  - Reconciliation mismatch detection
  - Exposure invariant violation attempt
- Paper trading report generator

### Tests

```bash
# Prerequisites: Phase 1-9 complete, infrastructure running

# 1. Run full integration test suite
make test-integration
# Expected: ALL tests pass

# 2. Run extended paper trading simulation
make simulate
# Let it run for 60 minutes
# Monitor in another terminal:
watch -n 10 'go run ./cmd/trade-ctl status && echo "---" && go run ./cmd/trade-ctl risk'

# 3. After 60 minutes, check results
go run ./cmd/trade-ctl status
go run ./cmd/trade-ctl risk
go run ./cmd/trade-ctl orders
go run ./cmd/trade-ctl ledger

# 4. Verify no errors in logs
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT event_type, severity, timestamp FROM audit.event_log
      WHERE severity = 'critical' ORDER BY timestamp DESC LIMIT 10;"

# 5. Verify hash chain integrity
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT COUNT(*) as total,
             COUNT(DISTINCT entry_hash) as unique_hashes
      FROM audit.event_log;"
# Expected: total = unique_hashes (no hash collisions)

# 6. Verify intent ledger invariants held throughout run
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT COUNT(*) as total_intents,
             MAX(version) - MIN(version) + 1 as expected_if_gapless
      FROM execution.order_intents;"
# Expected: total_intents = expected_if_gapless (no gaps)

# 7. Kill all services and verify recovery
pkill -f "go run ./cmd/"
sleep 5
make simulate &
sleep 10
go run ./cmd/trade-ctl status
# Expected: system recovers to correct state, any pre-crash halts are active

# 8. Certification: run the automated test scenarios
go test ./tests/integration/... -tags=integration -v -count=1 -timeout 10m
# Expected: ALL tests pass

# 9. Generate paper trading report
docker compose exec postgres psql -U trader -d autonomy -c "
  SELECT
    (SELECT COUNT(*) FROM execution.orders) as total_orders,
    (SELECT COUNT(*) FROM execution.orders WHERE status = 'filled') as filled,
    (SELECT COUNT(*) FROM execution.orders WHERE status = 'cancelled') as cancelled,
    (SELECT COUNT(*) FROM execution.orders WHERE status = 'rejected') as rejected,
    (SELECT COUNT(*) FROM risk.policy_decisions WHERE decision = 'approved') as approved,
    (SELECT COUNT(*) FROM risk.policy_decisions WHERE decision = 'denied') as denied,
    (SELECT COUNT(*) FROM execution.order_intents) as total_intents,
    (SELECT COUNT(*) FROM watchdog.kill_switch_events) as kill_switches,
    (SELECT COUNT(*) FROM audit.event_log) as audit_entries;
"
# Review numbers: are they sensible? Any unexpected zeros?

# 10. Cleanup
pkill -f "go run ./cmd/" 2>/dev/null || true
```

### Exit Criteria

Paper trading certification checklist — all must pass:
- [ ] All 20 risk checks fire correctly
- [ ] Kill switch blocks orders within 1 second
- [ ] Dead man's switch fires within configured timeout
- [ ] Orders survive process restart
- [ ] Risk state survives process restart
- [ ] Intent ledger versions are gapless
- [ ] Exposure state reconstructable from ledger replay
- [ ] Halts survive process restart
- [ ] Reconciliation detects injected mismatches
- [ ] Audit log chain is intact (no hash collisions)
- [ ] All operator CLI commands work
- [ ] System runs for 1 hour without errors

### Commit Checkpoint
```
feat: Phase 10 — full integration testing and paper trading certification

- Extended integration test suite covering all failure scenarios
- Automated test harness: normal flow, kill switch, dead man's switch,
  stale data, duplicates, fat finger, position limits, crash recovery,
  exposure invariants, intent ledger replay
- Paper trading certification checklist with all items verified
- 60-minute stability test completed successfully
- Audit chain integrity verified
- Intent ledger gapless invariant verified
- System recovery after full restart verified

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
```

---

## Phase 11: Real Kalshi Data (Shadow Mode — Still No Real Money)

### Goal
Replace the mock data feed with real Kalshi market data. The system reads real prices, generates real signals, evaluates real orders through the full risk pipeline, but does NOT submit to the real exchange. Orders go to the mock exchange as before.

### Dependencies
- Phase 10 (paper trading certification passed)
- Kalshi read-only API key obtained

### Build Items
- `pkg/kalshi/client.go` — Kalshi REST API client (read-only)
- `services/data/kalshi_feed.go` — real Kalshi market data ingestion
- Configuration to select data source: `DATA_SOURCE=mock` or `DATA_SOURCE=kalshi`
- Read-only API key for Kalshi (no trading permissions)
- Market mapping: Kalshi tickers to internal market IDs
- Validation: real data passes same freshness/depth checks as mock data
- Rate limiting: respects Kalshi's 10 req/sec limit

### Tests

```bash
# Prerequisites: Phase 1-10 complete, Kalshi read-only API key obtained
# Store the API key in .env (NOT the trading key):
# KALSHI_API_KEY_ID=your_read_key_id
# KALSHI_API_KEY_SECRET=your_read_key_secret

# 1. Compile Kalshi client
go build ./pkg/kalshi/...
# Expected: no errors

# 2. Test Kalshi API connectivity (read-only)
go test ./pkg/kalshi/ -v -run TestListMarkets
# Expected: returns list of active markets

# 3. Run data ingestion with real data
DATA_SOURCE=kalshi go run ./cmd/data-ingestion &
DATA_PID=$!
sleep 10
# Expected: real market data flowing to NATS
kill $DATA_PID

# 4. Run full system in shadow mode
DATA_SOURCE=kalshi make simulate &
sleep 60
go run ./cmd/trade-ctl status
go run ./cmd/trade-ctl risk
go run ./cmd/trade-ctl orders
# Expected: real market IDs in orders, real prices, all risk checks working
# Orders submitted to mock exchange (not real Kalshi)

# 5. Verify data quality checks work with real data
docker compose exec postgres psql -U trader -d autonomy \
  -c "SELECT decision, checks_json->0->>'name' as first_check
      FROM risk.policy_decisions ORDER BY decided_at DESC LIMIT 10;"
# Expected: some orders denied for data_freshness, spread_check, etc.
# based on real market conditions

# 6. Verify NO real orders were placed
# Check Kalshi account: 0 orders, 0 positions
# The system used read-only credentials — it CAN'T trade even if it tried

# 7. Cleanup
pkill -f "go run ./cmd/" 2>/dev/null || true
```

### Exit Criteria
- Real Kalshi market data flows through the system
- All existing risk checks work unchanged with real data
- No real orders placed (read-only credentials enforced)
- System runs for 24 hours in shadow mode without errors
- Real market conditions trigger appropriate risk denials (spreads, staleness)

### Commit Checkpoint
```
feat: Phase 11 — real Kalshi market data in shadow mode

- Kalshi REST API client (read-only operations only)
- Real market data ingestion with configurable source (mock/kalshi)
- Rate limiting respecting Kalshi's 10 req/sec
- Real data normalized to internal MarketData format
- All risk checks validated against real market conditions
- Shadow mode: real data, real signals, mock execution (no real trades)
- Credential isolation: read-only API key, no trading capability

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
```

---

## What Comes After Phase 11

The following phases require real money and are NOT part of the initial build. Each requires explicit approval and a formal review of all prior phase test results.

### Phase 12: Live Trading with Minimal Capital ($100–500) — Kalshi Only
- Replace mock exchange adapter with real Kalshi trading adapter
- Trading API credential (separate from data credential)
- IP-restricted API key
- Production risk limits (100x tighter than paper)
- Restricted market universe (2–3 markets)
- Maximum 10 trades per day
- Intent ledger becomes the single source of truth for real exposure
- **Requires: all Phase 1–11 tests passing, 2 weeks of clean shadow mode**

### Phase 13: Polymarket Integration
- Wallet-based authentication (EIP-712 signing)
- Signing service with HSM/KMS integration
- Hot wallet with minimal capital
- CLOB API adapter
- WebSocket fill notifications
- Venue-specific intent ledger entries (venue field on intents)
- Cross-venue exposure aggregation in risk engine
- **Requires: Phase 12 stable for 2+ weeks on Kalshi**

### Phase 14: Production Hardening
- mTLS between all services
- Vault for credential management
- S3 Object Lock for immutable audit logs
- Kubernetes deployment
- PagerDuty alerting integration
- Credential rotation automation
- **Requires: incident response drill completed**

### Phase 15: Scaled Production
- Increased limits based on track record
- Multiple strategies
- Cross-venue exposure management (Kalshi + Polymarket)
- Operator web dashboard
- Second operator trained
- **Requires: 90-day track record, formal review**
