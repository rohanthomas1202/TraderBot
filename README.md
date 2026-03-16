<p align="center">
  <h1 align="center">TraderBot</h1>
  <p align="center">
    Autonomous prediction-market trading system for Kalshi
    <br />
    <em>Paper trade safely. Shadow trade with real data. Go live with confidence.</em>
  </p>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.25-00ADD8?style=flat-square&logo=go" alt="Go 1.25" />
  <img src="https://img.shields.io/badge/Python-3.11+-3776AB?style=flat-square&logo=python&logoColor=white" alt="Python 3.11+" />
  <img src="https://img.shields.io/badge/PostgreSQL-16-4169E1?style=flat-square&logo=postgresql&logoColor=white" alt="PostgreSQL 16" />
  <img src="https://img.shields.io/badge/gRPC-Protobuf-244c5a?style=flat-square&logo=grpc" alt="gRPC" />
  <img src="https://img.shields.io/badge/License-Private-red?style=flat-square" alt="License" />
</p>

---

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Tech Stack](#tech-stack)
- [Getting Started](#getting-started)
- [Usage](#usage)
- [Services](#services)
- [Risk Controls](#risk-controls)
- [Operator CLI](#operator-cli)
- [Monitoring](#monitoring)
- [Testing](#testing)
- [Roadmap](#roadmap)

---

## Overview

TraderBot is a multi-service autonomous trading platform built for prediction markets. It implements a phased deployment model — paper trading first, then shadow mode with real market data, then live execution — so every component is battle-tested before real capital is at risk.

**Key design principles:**

- **Safety first** — HMAC-signed order approvals, kill switches, hash-chained audit logs
- **Phased rollout** — each phase is independently deployable and testable
- **One source of truth** — PostgreSQL is the system of record for all state
- **Microservice architecture** — services communicate via gRPC with protobuf contracts
- **Event-driven** — NATS JetStream for asynchronous market data and system events

---

## Architecture

```mermaid
flowchart TB
    subgraph Data Layer
        DI[Data Ingestion]
        KA[Kalshi API]
        ME[Mock Exchange]
    end

    subgraph Decision Layer
        SE[Strategy Engine]
        RE[Risk Engine]
    end

    subgraph Execution Layer
        EE[Execution Engine]
    end

    subgraph Safety Layer
        WD[Watchdog]
        RC[Reconciliation]
    end

    subgraph Infrastructure
        PG[(PostgreSQL)]
        NATS[NATS JetStream]
        PROM[Prometheus]
        GRAF[Grafana]
    end

    KA -->|market data| DI
    ME -->|mock data| DI
    DI -->|publish| NATS
    NATS -->|subscribe| SE
    SE -->|evaluate order| RE
    RE -->|signed approval| SE
    SE -->|submit order| EE
    EE -->|place order| ME
    EE -->|place order| KA
    EE -->|heartbeat| WD
    WD -->|halt signal| EE
    RC -->|verify| PG
    EE --> PG
    RE --> PG
    WD --> PG
    PROM -->|scrape| GRAF
```

### Order Flow

```
Strategy detects signal
    → RiskEngine.EvaluateOrder() — checks 40+ policy rules, signs approval
        → ExecutionEngine.SubmitOrder() — writes to DB, sends to venue
            → Venue fills order
                → FillPolling detects fill → updates positions & risk state
```

---

## Tech Stack

| Layer | Technology |
|-------|-----------|
| **Backend** | Go 1.25, gRPC, Protobuf |
| **ML / Backtesting** | Python 3.11+, NumPy, SciPy, scikit-learn, pandas |
| **Database** | PostgreSQL 16 |
| **Messaging** | NATS JetStream |
| **Monitoring** | Prometheus + Grafana |
| **Infrastructure** | Docker Compose |
| **Markets** | Kalshi (primary), Polymarket (planned) |

---

## Getting Started

### Prerequisites

- Go 1.25+
- Python 3.11+
- Docker & Docker Compose
- Make
- Kalshi API credentials (for shadow/live mode)

### Setup

```bash
# Clone the repository
git clone https://github.com/your-username/TraderBot.git
cd TraderBot

# Copy environment config
cp .env.example .env
# Edit .env with your credentials

# Start infrastructure (Postgres + NATS)
make up

# Run database migrations
make migrate

# Build all services
make build
```

### Quick Start (Paper Trading)

```bash
# Start all services in paper trading mode
make simulate

# In another terminal — check system status
make status

# View open orders
make orders

# View risk state
make risk
```

---

## Usage

### Modes of Operation

| Mode | Data Source | Execution Venue | Use Case |
|------|-----------|----------------|----------|
| **Paper** | Mock feed | Mock exchange | Development & testing |
| **Shadow** | Kalshi API | Mock exchange | Validate signals with real data |
| **Live** | Kalshi API | Kalshi | Real trading (future) |

### Configuration

All configuration flows through environment variables and a risk policy YAML:

```bash
# Key environment variables
EXECUTION_MODE=paper          # paper | live
DATA_SOURCE=mock              # mock | kalshi
POLICY_FILE=policies/paper.yaml
POSTGRES_URL=postgres://trader:trader@localhost:5432/autonomy
NATS_URL=nats://localhost:4222
```

---

## Services

| Service | Port | Description |
|---------|------|-------------|
| **Data Ingestion** | `50010` | Publishes market data to NATS from mock feed or Kalshi |
| **Risk Engine** | `50020` | Evaluates orders against 40+ policy checks, signs approvals |
| **Strategy Engine** | `50030` | Generates trading signals (simple momentum) |
| **Execution Engine** | `50040` | Manages order lifecycle, communicates with venues |
| **Watchdog** | `50055` | Kill switch, heartbeat monitoring, crash recovery |
| **Dashboard** | `8080` | Real-time web UI |
| **Prometheus** | `9090` | Metrics collection |
| **Grafana** | `3000` | Dashboards & alerting |

---

## Risk Controls

TraderBot enforces multiple layers of safety:

- **Per-trade limits** — max notional, max quantity per order
- **Per-strategy limits** — daily loss caps, max orders/minute, consecutive loss limits
- **Global kill switches** — triggered by daily loss, drawdown, or heartbeat timeout
- **HMAC-signed approvals** — execution engine verifies risk engine signatures before placing orders
- **Hash-chained audit log** — tamper-evident record of every decision
- **Order intent ledger** — enforces that open orders + positions never exceed limits

### Kill Switch Levels

| Level | Behavior |
|-------|----------|
| `soft_pause` | Reject new orders, let existing fills complete |
| `cancel_only` | Cancel all pending orders |
| `full_stop` | Emergency halt — all trading ceases immediately |

---

## Operator CLI

`trade-ctl` provides break-glass operational control:

```bash
trade-ctl status                  # System state & active halts
trade-ctl orders                  # List open orders
trade-ctl risk                    # Current exposure & daily P&L
trade-ctl kill --level full_stop  # Emergency halt
trade-ctl ack --cause "..."       # Acknowledge halt
trade-ctl resume                  # Resume trading
trade-ctl audit                   # View audit trail
trade-ctl trace <trace_id>        # Reconstruct full order flow
```

Or use Makefile shortcuts:

```bash
make status    make orders    make risk
make kill      make audit     make ledger
```

---

## Monitoring

```bash
# Start full monitoring stack
make monitor
```

- **Prometheus** at `localhost:9090` — metrics from all services
- **Grafana** at `localhost:3000` — pre-configured dashboards
- **Telegram alerts** via `make alertbot`

---

## Testing

```bash
# Unit tests
make test

# Integration tests (Phase 10 certification)
make test-integration

# Chaos / fault-injection tests
make chaos

# Backtest a strategy
make backtest STRATEGY=simple-momentum FROM=2025-01-01 TO=2025-06-01
```

### Integration Test Coverage

- End-to-end order lifecycle
- Kill switch trigger & recovery
- Order intent ledger monotonicity
- Crash recovery scenarios
- Database & NATS failure handling

---

## Roadmap

<details>
<summary><strong>Completed Phases</strong></summary>

| Phase | Description | Status |
|-------|-------------|--------|
| 1 | Foundation infrastructure | Done |
| 2 | Risk engine unit tests | Done |
| 3 | Order intent ledger | Done |
| 4 | Execution engine | Done |
| 5 | Watchdog unit tests | Done |
| 6 | Strategy unit tests | Done |
| 7 | gRPC wiring | Done |
| 8 | Reconciliation engine | Done |
| 9 | Operator CLI | Done |
| 10 | Integration testing & paper trading certification | Done |
| 11 | Kalshi shadow mode (real data, mock execution) | Done |

</details>

### Upcoming

| Phase | Description |
|-------|-------------|
| 12 | Live execution on Kalshi |
| 13 | Polymarket venue adapter |
| 14 | Multi-strategy portfolio |
| 15 | Advanced ML signal pipeline |
| 16 | Production hardening |

### Future Features

- **F1** — Real-time web dashboard
- **F2** — Mobile alerts (Telegram/Slack)
- **F3** — Advanced backtesting framework
- **F4** — ML signal pipeline
- **F5** — Grafana observability suite
- **F6** — Chaos testing framework

---

## Project Structure

```
TraderBot/
├── cmd/                  # Service entry points
│   ├── risk-engine/
│   ├── execution-engine/
│   ├── strategy-engine/
│   ├── data-ingestion/
│   ├── watchdog/
│   ├── trade-ctl/        # Operator CLI
│   ├── data-scraper/     # Kalshi data collection
│   ├── dashboard/        # Web UI
│   └── mock-exchange/    # Paper trading venue
├── internal/             # Shared Go packages
│   ├── models/           # Core domain types
│   ├── audit/            # Hash-chained audit logging
│   ├── events/           # NATS pub/sub
│   ├── ledger/           # Order intent ledger
│   └── ...
├── services/             # Service implementations
├── proto/                # Protobuf definitions
├── gen/                  # Generated gRPC code
├── python/               # ML models & backtesting
│   ├── backtesting/      # Strategy backtester
│   ├── market_logger/    # Kalshi orderbook collector
│   └── models/           # ML models (EMOS weather)
├── migrations/           # PostgreSQL migrations
├── configs/              # Dev & market configs
├── policies/             # Risk policy YAML
├── monitoring/           # Prometheus & Grafana configs
├── tests/                # Integration & chaos tests
├── docker-compose.yml
├── Dockerfile
└── Makefile
```

---

<p align="center">
  <sub>Built with discipline. Trade with confidence.</sub>
</p>
