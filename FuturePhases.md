# Future Phases — Post-Core Build

These phases are additive features to be built after the core platform (Phases 1–16) is stable. Each is independent unless noted. Prioritize based on whether the goal is portfolio/resume or revenue.

---

## Phase F1: Real-Time Web Dashboard

### Goal
A browser-based dashboard providing a single-screen overview of the entire system: live P&L, positions, exposure, order flow, risk state, and kill switch controls.

### Build Items
- Lightweight Go HTTP/WebSocket server reading from PostgreSQL and NATS
- Frontend (React or plain HTML + JS) with live-updating components
- P&L curves, position heatmaps, order flow timeline
- Kill switch button — one click to halt everything
- Authentication layer (API key or session-based)

### Why
Replaces the need for multiple terminal windows. Makes anomalies visible at a glance. Shareable with non-technical stakeholders.

---

## Phase F2: Mobile Alerts & Remote Controls

### Goal
Push notifications and lightweight controls from a mobile device, so the operator can monitor and intervene without being at a terminal.

### Build Items
- Telegram or Slack bot with commands: `/status`, `/kill`, `/resume`, `/pnl`
- Push notifications on: fills, drawdown warnings, kill switch triggers, strategy halts
- Approve/deny high-risk trades from phone
- Rate limiting and authentication on bot commands

### Dependencies
- Phase F1 (optional — can work standalone via bot)
- Phase 5 (watchdog/kill switch)

### Why
Trading doesn't stop when you step away. Critical alerts need to reach you immediately.

---

## Phase F3: Strategy Backtesting Engine

### Goal
Replay historical market data through the real risk engine and strategy code. Same code path as live trading — no separate backtest framework.

### Build Items
- Historical data ingestion and storage (OHLCV, orderbook snapshots)
- Time-simulation harness that replays data through the strategy and risk engine
- Performance metrics: Sharpe ratio, max drawdown, win rate, profit factor
- Backtest result storage and comparison across parameter sweeps
- CLI command: `trade-ctl backtest --strategy=mean-revert --from=2025-01-01 --to=2025-06-01`

### Why
Never risk capital on an untested strategy. Backtesting with the real risk engine catches limit violations before they happen live.

---

## Phase F4: ML Signal Pipeline

### Goal
A feature store and model serving layer that feeds prediction signals into strategies. Shadow mode ensures signals are validated before trading on them.

### Build Items
- Feature store: news sentiment, social data, on-chain metrics, historical price features
- Model serving endpoint (Python sidecar or Go inference via ONNX)
- Shadow mode: signals logged and scored but not traded until accuracy threshold met
- A/B testing framework: split capital between signal-driven and baseline strategies
- Automatic model retraining pipeline with drift detection

### Dependencies
- Phase F3 (backtesting — validate models offline first)

### Why
Discretionary signals don't scale. ML models can process more data and react faster. Shadow mode makes this safe to experiment with.

---

## Phase F5: Grafana + Prometheus Observability

### Goal
Production-grade monitoring with metrics, alerting, and pre-built dashboards.

### Build Items
- Prometheus metrics exporter in each Go service (latency histograms, counters, gauges)
- Key metrics: order-to-fill latency, risk check duration, fill rate, P&L per strategy
- Grafana dashboards: system overview, per-strategy performance, infrastructure health
- Alert rules: "P95 latency > 500ms", "fill rate < 80%", "service unhealthy for 30s"
- Docker Compose addition: Prometheus + Grafana containers

### Why
You can't improve what you can't measure. Alerting catches degradation before it becomes an incident. Dashboards look professional in demos.

---

## Phase F6: Chaos Testing Framework

### Goal
Automated fault injection to prove the system recovers gracefully from infrastructure failures.

### Build Items
- Chaos scenarios: kill random services, inject network partitions, simulate exchange outages
- NATS disconnection and reconnection testing
- PostgreSQL failover simulation
- Partial fill + timeout recovery under degraded conditions
- Automated chaos test suite: `make chaos`
- Recovery verification: system state matches expected state after each scenario

### Why
Impressive in interviews and demos — "here's what happens when NATS dies mid-order." Builds real confidence that the safety net works.

---

## Phase F7: Audit Replay & Forensics Tool

### Goal
Given any time range, reconstruct exactly what the system saw, decided, and did. A visual timeline for debugging, compliance, and post-mortems.

### Build Items
- Replay engine: reads audit log, risk decisions, intent ledger, fills for a time window
- Visual timeline: market data → risk decision → intent → order → fill
- Diff view: "what would have happened under different risk limits?"
- Export to JSON/CSV for external analysis
- CLI command: `trade-ctl replay --from="2025-03-15 14:00" --to="2025-03-15 14:30"`

### Dependencies
- Phase 3 (intent ledger)
- Phase 8 (reconciliation)

### Why
When something goes wrong, you need to know exactly why within minutes. Also valuable for regulatory compliance if the platform scales.

---

## Phase F8: Multi-Strategy Capital Allocation

### Goal
Automatic capital allocation across strategies based on performance, with dynamic rebalancing.

### Build Items
- Kelly criterion or risk-parity allocation engine
- Strategy performance tracker: rolling Sharpe, drawdown, win rate
- Automatic rebalancing — underperforming strategies get less capital
- Strategy leaderboard with live performance metrics
- Configurable allocation constraints (min/max per strategy, ramp-up period for new strategies)

### Dependencies
- Phase 7 (multi-service integration)

### Why
Manual capital allocation doesn't scale. The best strategies should automatically get more capital, and struggling ones should be throttled before they hit loss limits.

---

## Phase F9: Cross-Venue Arbitrage

### Goal
Detect and execute arbitrage opportunities where the same event is priced differently across venues (Kalshi vs Polymarket).

### Build Items
- Cross-venue price monitor: real-time spread tracking between venues
- Atomic execution logic: buy on one venue, sell on the other
- Exposure netting: the intent ledger tracks net exposure across venues
- Slippage and fee-aware profitability calculator
- Safety: only execute when both legs can be filled with high confidence

### Dependencies
- Phase 13 (Polymarket adapter)
- Phase F8 (capital allocation — arb strategies need dedicated capital)

### Why
Pure arbitrage is the closest thing to risk-free profit. The intent ledger already supports multi-venue exposure tracking, making this a natural extension.

---

## Phase F10: Options-Style Risk Analytics

### Goal
Greeks-equivalent metrics for prediction markets, enabling portfolio-level risk management beyond simple notional limits.

### Build Items
- Delta-to-probability: sensitivity of position value to probability changes
- Time decay (theta equivalent): how position value changes as event approaches
- Portfolio-level VaR (Value at Risk) with Monte Carlo simulation
- Correlation tracking between markets (e.g., related political events)
- Risk dashboard integration showing Greeks per position and portfolio-level aggregates

### Dependencies
- Phase F1 (dashboard — Greeks need visualization)
- Phase F5 (observability — metrics feed into risk calculations)

### Why
Notional limits are blunt. A $5,000 position at 90% probability is very different from $5,000 at 50%. Greeks-style analytics let you manage actual risk, not just dollar exposure.

---

## Prioritization Guide

| Goal | Start with |
|------|-----------|
| Portfolio / resume | F5 (Grafana) → F6 (Chaos) → F1 (Dashboard) → F3 (Backtest) |
| Making money | F3 (Backtest) → F4 (ML Signals) → F8 (Capital Allocation) → F9 (Arbitrage) |
| Operational safety | F5 (Grafana) → F2 (Mobile Alerts) → F7 (Forensics) → F6 (Chaos) |
| Impressiveness | F1 (Dashboard) → F6 (Chaos) → F10 (Greeks) → F4 (ML Signals) |
