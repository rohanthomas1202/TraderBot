# Prediction Market Trading System

## Profit-Maximizing Architecture and Build Plan

**Document type:** Internal engineering architecture specification
**System:** Autonomous prediction-market trading platform for Kalshi and Polymarket
**Codebase:** `autonomy-platform` (Go 1.23)
**Current state:** Phase 9 complete — execution, risk, reconciliation, watchdog, operator CLI operational with mock exchange
**Objective:** Maximize long-term trading profit through superior probability estimation across multiple prediction-market verticals

---

## Section 1 — End-State Architecture

### 1.1 Architecture Overview

The system is a multi-vertical prediction-market trading platform. Each market category (weather, economic, crypto, sports) runs an independent forecasting model. A central signal aggregator ranks opportunities across categories and allocates capital. The existing Go execution infrastructure handles risk checks, order management, and venue interaction.

The architecture separates along a language boundary: **Python owns intelligence** (data ingestion, modeling, signal generation). **Go owns execution** (risk, orders, reconciliation, venue adapters). **NATS JetStream** is the boundary.

### 1.2 Architecture Diagram

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                           DATA INGESTION LAYER (Python)                      │
│                                                                              │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐             │
│  │  Weather Pod     │  │  Economic Pod    │  │  Crypto Pod      │  + more    │
│  │                  │  │                  │  │                  │            │
│  │  gfs_fetch()     │  │  fred_fetch()    │  │  cex_ws_feed()   │            │
│  │  ecmwf_fetch()   │  │  bls_fetch()     │  │  onchain_poll()  │            │
│  │  noaa_obs()      │  │  consensus_api() │  │  deribit_iv()    │            │
│  │  nam_hrrr()      │  │  regional_fed()  │  │  funding_rates() │            │
│  │                  │  │                  │  │                  │            │
│  │  Schedule: 6h    │  │  Schedule: event │  │  Schedule: 10s   │            │
│  └───────┬──────────┘  └───────┬──────────┘  └───────┬──────────┘            │
│          │                     │                      │                      │
│          └─────────────────────┼──────────────────────┘                      │
│                                ▼                                             │
│                    NATS JetStream (existing)                                 │
│                    data.weather.* | data.econ.* | data.crypto.*              │
└────────────────────────────────┬─────────────────────────────────────────────┘
                                 │
┌────────────────────────────────┼─────────────────────────────────────────────┐
│                        INTELLIGENCE LAYER (Python)                           │
│                                                                              │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐           │
│  │  Weather Model    │  │  Econ Model       │  │  Crypto Model    │  + more  │
│  │                   │  │                   │  │                  │          │
│  │  EMOS + isotonic  │  │  Surprise dist +  │  │  Implied vol +   │          │
│  │  calibration      │  │  hi-freq adj      │  │  on-chain sigs   │          │
│  │                   │  │                   │  │                  │          │
│  │  Output:          │  │  Output:          │  │  Output:         │          │
│  │  {market_id,      │  │  {market_id,      │  │  {market_id,     │          │
│  │   P(event),       │  │   P(event),       │  │   P(event),      │          │
│  │   uncertainty,    │  │   uncertainty,    │  │   uncertainty,   │          │
│  │   edge_vs_mkt}    │  │   edge_vs_mkt}    │  │   edge_vs_mkt}   │          │
│  └────────┬─────────┘  └────────┬─────────┘  └────────┬─────────┘          │
│           └──────────────────────┼─────────────────────┘                     │
│                                  ▼                                           │
│  ┌───────────────────────────────────────────────────────────────────┐       │
│  │                SIGNAL AGGREGATOR + CAPITAL ALLOCATOR               │       │
│  │                                                                    │       │
│  │  1. Collect signals from all category models                      │       │
│  │  2. Rank by fee-adjusted edge (net_edge = edge - spread - fees)   │       │
│  │  3. Apply Kelly fraction for position sizing                      │       │
│  │  4. Check capital availability per category                       │       │
│  │  5. Apply correlation penalty for same-event contracts            │       │
│  │  6. Apply opportunity-cost adjustment                             │       │
│  │  7. Publish final sized signals to NATS                           │       │
│  └───────────────────────────────┬───────────────────────────────────┘       │
│                                  │                                           │
│  ┌───────────────────────────────────────────────────────────────────┐       │
│  │                CROSS-CATEGORY EVENT ROUTER                        │       │
│  │                                                                    │       │
│  │  Rule-based: hurricane → weather + econ + insurance contracts     │       │
│  │  Detects events relevant to multiple categories                   │       │
│  │  Boosts signal priority for correlated opportunities              │       │
│  └───────────────────────────────────────────────────────────────────┘       │
│                                                                              │
│  ┌───────────────────────────────────────────────────────────────────┐       │
│  │                CALIBRATION TRACKER (per model)                    │       │
│  │                                                                    │       │
│  │  Records (prediction, outcome) pairs                              │       │
│  │  Rolling 30-day Brier score                                       │       │
│  │  Triggers RECALIBRATE or HALT via NATS                            │       │
│  └───────────────────────────────────────────────────────────────────┘       │
└──────────────────────────────────┬───────────────────────────────────────────┘
                                   │ NATS: signals.aggregated
                                   ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                     EXECUTION LAYER (existing Go infrastructure)             │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────┐        │
│  │  ExternalSignal SignalFunc ← NATS signals.aggregated             │        │
│  │  (new: replaces SimpleMomentum for live strategies)              │        │
│  └──────────────────────┬───────────────────────────────────────────┘        │
│                         │                                                    │
│  ┌──────────────────────▼───────────────────────────────────────────┐        │
│  │  Risk Engine (existing 20 checks)                                │        │
│  │  + per-model risk attribution (new)                              │        │
│  │  + category exposure limits (new)                                │        │
│  │  + model degradation kill switch trigger (new)                   │        │
│  └──────────────────────┬───────────────────────────────────────────┘        │
│                         │                                                    │
│  ┌──────────────────────▼───────────────────────────────────────────┐        │
│  │  Execution Engine (existing)                                     │        │
│  │  Intent ledger → HMAC verification → venue submission            │        │
│  │  VenueAdapter: MockAdapter | KalshiAdapter | PolymarketAdapter   │        │
│  └──────────────────────┬───────────────────────────────────────────┘        │
│                         │                                                    │
│  ┌──────────────────────▼───────────────────────────────────────────┐        │
│  │  Watchdog (existing + model degradation triggers)                │        │
│  │  Reconciliation (existing + cross-venue awareness)               │        │
│  └──────────────────────────────────────────────────────────────────┘        │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────┐        │
│  │  Venue Adapters                                                  │        │
│  │  ├── MockAdapter (existing, services/execution/paper_adapter.go) │        │
│  │  ├── KalshiAdapter (new, pkg/kalshi/)                            │        │
│  │  └── PolymarketAdapter (new, pkg/polymarket/)                    │        │
│  └──────────────────────────────────────────────────────────────────┘        │
└──────────────────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────────────────┐
│                              STORAGE LAYER                                   │
│                                                                              │
│  PostgreSQL 16 (existing)                                                    │
│  ├── execution schema (orders, fills, order_intents)                         │
│  ├── risk schema (positions, daily_stats, policy_decisions, limits)           │
│  ├── watchdog schema (kill_switch_events, heartbeats)                        │
│  ├── audit schema (event_log, operator_actions, config_changes)              │
│  ├── market_data schema (new: orderbook_snapshots, contract_resolutions)     │
│  └── models schema (new: predictions, calibration_scores, model_versions)    │
│                                                                              │
│  Parquet files (new: historical backtest data, GFS reforecast archives)      │
│                                                                              │
│  NATS JetStream (existing)                                                   │
│  ├── ORDERS stream: order.> (existing)                                       │
│  ├── RISK stream: risk.> (existing)                                          │
│  ├── KILL stream: kill.> (existing)                                          │
│  ├── SYSTEM stream: system.> (existing)                                      │
│  ├── DATA stream: data.> (existing)                                          │
│  ├── SIGNALS stream: signals.> (new)                                         │
│  └── CALIBRATION stream: calibration.> (new)                                 │
└──────────────────────────────────────────────────────────────────────────────┘
```

### 1.3 Language Boundary

| Domain | Language | Rationale |
|--------|----------|-----------|
| Data collection, parsing, modeling | Python | Data science ecosystem (scipy, sklearn, xarray, eccodes), not latency-critical |
| Signal aggregation, capital allocation | Python | Optimization math (scipy.optimize), portfolio-level reasoning |
| Risk evaluation, order management | Go | Already built, latency-sensitive, type-safe |
| Venue adapters, reconciliation | Go | Network I/O, state machines, reliability-critical |
| Message passing | NATS JetStream | Already in stack, decouples Python from Go |

### 1.4 Process Inventory (End State)

| Process | Language | Schedule | Failure Impact |
|---------|----------|----------|----------------|
| weather_model | Python | Every 60s (model update on GFS cycle) | Lose weather signals |
| econ_model | Python | Event-driven (release schedule) + 5min poll | Lose econ signals |
| crypto_model | Python | Every 10s | Lose crypto signals |
| sports_model | Python | Every 5min during game days | Lose sports signals |
| signal_aggregator | Python | Continuous (consumes model outputs) | All signal flow stops — kill switch |
| strategy-engine | Go | Existing (consumes aggregated signals) | Order flow stops |
| risk-engine | Go | Existing | All orders blocked |
| execution-engine | Go | Existing | No venue submission |
| watchdog | Go | Existing | No kill switch |
| data-ingestion | Go | Existing (mock data for testing) | No mock market data |
| postgres | — | Always on | Full system down |
| nats | — | Always on | Full system down |

---

## Section 2 — System Components

### 2.1 Data Ingestion Services

Each market category runs a self-contained Python process that fetches, parses, normalizes, and publishes data to NATS. No shared infrastructure between collectors — each is independently deployable and restartable.

#### Weather Data Pod

```
collectors/weather/
├── __init__.py
├── weather_pod.py          # Main process: scheduler + NATS publisher
├── gfs_fetcher.py          # GFS ensemble (31 members, 0.25° grid)
├── ecmwf_fetcher.py        # ECMWF via Open-Meteo API (51 members)
├── noaa_obs_fetcher.py     # NOAA ISD station observations
├── nam_hrrr_fetcher.py     # NAM/HRRR mesoscale (optional, Phase 3)
└── parsers/
    ├── grib_parser.py      # GRIB2 → numpy arrays (eccodes/cfgrib)
    └── station_parser.py   # ISD format → structured observations
```

**Data sources:**
- GFS ensemble: NOAA NOMADS (free, HTTPS, GRIB2 format, 6-hour cycle at 00/06/12/18 UTC)
- ECMWF: Open-Meteo API (free tier, JSON, 6-hour cycle)
- NOAA observations: ISD/METAR (free, updated every 5-60 min per station)
- NAM/HRRR: NOAA NOMADS (free, higher resolution US-only, 1-hour cycle)

**NATS subjects:**
- `data.weather.gfs.{location}` — GFS ensemble forecast
- `data.weather.ecmwf.{location}` — ECMWF ensemble forecast
- `data.weather.obs.{station}` — Station observations

**Message schema:**
```json
{
  "source": "gfs_ensemble",
  "location": "KNYC",
  "valid_time": "2026-03-20T18:00:00Z",
  "forecast_time": "2026-03-15T06:00:00Z",
  "metric": "max_temperature_2m",
  "unit": "fahrenheit",
  "ensemble": {
    "members": [82.1, 84.3, 85.0, ...],
    "mean": 84.2,
    "std": 3.1,
    "p10": 79.5,
    "p50": 84.0,
    "p90": 88.8
  },
  "quality": {
    "member_count": 31,
    "completeness": 1.0,
    "fetch_latency_ms": 2340
  }
}
```

#### Economic Data Pod

```
collectors/economic/
├── __init__.py
├── econ_pod.py             # Main process: scheduler + NATS publisher
├── fred_fetcher.py         # FRED API (GDP, CPI, NFP, unemployment)
├── bls_fetcher.py          # BLS release scraper
├── consensus_fetcher.py    # Consensus estimates (Bloomberg/FocusEconomics)
└── release_schedule.py     # Known release dates for economic indicators
```

**Data sources:**
- FRED: St. Louis Fed API (free, requires API key)
- BLS: Public release calendar + data (free)
- Consensus: FocusEconomics API or Bloomberg terminal scrape ($0-200/mo)

**NATS subjects:**
- `data.econ.release.{indicator}` — Actual release values
- `data.econ.consensus.{indicator}` — Pre-release consensus estimates

#### Crypto Data Pod

```
collectors/crypto/
├── __init__.py
├── crypto_pod.py           # Main process: WebSocket feeds + NATS publisher
├── cex_feed.py             # Binance/Coinbase WebSocket price feeds
├── deribit_iv.py           # Deribit options implied volatility
├── onchain_fetcher.py      # On-chain metrics (exchange flows, whale alerts)
└── funding_rates.py        # Perpetual funding rates
```

**Data sources:**
- Binance WebSocket: Free, real-time, sub-second
- Deribit: Free API, options chain + implied vol
- On-chain: Glassnode (free tier) or Dune Analytics (free)

**NATS subjects:**
- `data.crypto.price.{pair}` — Live price feed
- `data.crypto.iv.{asset}` — Implied volatility surface
- `data.crypto.onchain.{metric}` — On-chain signals

### 2.2 Feature Pipeline

There is no separate feature store service. Each model process maintains its own in-memory feature state. This is correct because:
- Each model reads different features
- Feature computation is model-specific
- Cross-model feature sharing is not needed at this scale
- Eliminates Redis as a runtime dependency

Historical features for backtesting are stored as Parquet files, not in a database:
```
data/backtest/
├── weather/
│   ├── gfs_reforecast_2020_2025.parquet
│   ├── ecmwf_reforecast_2020_2025.parquet
│   └── noaa_observations_2020_2025.parquet
├── economic/
│   ├── fred_releases_2015_2025.parquet
│   └── consensus_estimates_2020_2025.parquet
└── crypto/
    ├── btc_ohlcv_2020_2025.parquet
    └── deribit_iv_surface_2022_2025.parquet
```

### 2.3 Forecasting Models

Each model is a Python class implementing a common interface:

```python
from dataclasses import dataclass
from abc import ABC, abstractmethod

@dataclass
class Forecast:
    market_id: str
    model_prob: float          # calibrated P(event)
    uncertainty: float         # ±1σ confidence band
    raw_prob: float            # pre-calibration probability
    model_version: str         # e.g., "weather-emos-v3"
    features_used: dict        # for audit/debug
    forecast_time: str         # ISO timestamp

@dataclass
class TradeSignal:
    market_id: str
    side: str                  # "buy" or "sell"
    quantity: int
    price_micros: int          # 0-1,000,000
    edge: float                # net edge after fees
    model_prob: float
    market_prob: float
    uncertainty: float
    model_version: str
    reason: str

class ForecastModel(ABC):
    @abstractmethod
    def forecast(self, contract: dict, features: dict) -> Forecast:
        """Produce calibrated probability for a single contract."""
        pass

    @abstractmethod
    def retrain(self, training_data) -> None:
        """Retrain model on updated data."""
        pass

    @abstractmethod
    def brier_score(self, window_days: int = 30) -> float:
        """Compute rolling Brier score for calibration monitoring."""
        pass
```

#### Weather Model: EMOS + Isotonic Calibration

**Algorithm:** Ensemble Model Output Statistics (EMOS) with Non-homogeneous Gaussian Regression (NGR).

**How it works:**
1. Combine GFS + ECMWF ensemble members into a single distribution
2. Fit parametric (Gaussian) distribution: μ = a + b₁×GFS_mean + b₂×ECMWF_mean, σ = c + d×ensemble_spread
3. Compute P(metric > threshold) from the fitted distribution
4. Pass through isotonic regression calibrator trained on historical (forecast, observation) pairs
5. Output calibrated probability with uncertainty band

**Training data:** GFS reforecast archive (2000-2025) + NOAA historical observations. Freely available from NOAA NOMADS.

**Retraining schedule:** Weekly batch retrain. Seasonal bias varies, so the model must track seasonal calibration.

**Key hyperparameters:**
- Calibration window: 2 years of training data
- Uncertainty floor: σ ≥ 0.5°F (prevents overconfidence)
- Probability clamp: 0.02 ≤ P ≤ 0.98 (never predict certainty)

#### Economic Model: Surprise Distribution

**Algorithm:** Student-t distribution fitted to historical forecast errors (surprises).

**How it works:**
1. Collect historical (consensus_estimate, actual_value) pairs for each indicator
2. Compute surprise = actual - consensus for each historical release
3. Fit Student-t distribution to surprises (captures fat tails)
4. For a new release: P(indicator > threshold) = survival function of Student-t centered at current consensus
5. Augment with analyst dispersion (wider dispersion → wider model distribution)
6. Apply isotonic calibration on historical backtest

**Training data:** FRED historical releases + consensus estimates. 20+ years for major indicators (NFP, CPI, GDP).

**Retraining schedule:** Monthly (surprise distribution is slow-moving).

#### Crypto Model: Implied Volatility + On-Chain Signals

**Algorithm:** Black-Scholes digital option pricing cross-referenced with on-chain momentum.

**How it works:**
1. Fetch Deribit options chain for the relevant asset and expiry
2. Extract implied volatility for the strike nearest to the contract threshold
3. Compute P(price > threshold) using Black-Scholes digital pricing
4. Adjust using on-chain momentum signal (exchange inflows/outflows as directional indicator)
5. Ensemble the options-implied probability with the on-chain-adjusted probability
6. Apply isotonic calibration

**Training data:** Deribit historical options data + on-chain data from Glassnode. 2-3 years sufficient.

**Retraining schedule:** Daily (crypto regimes shift fast).

#### Sports Model: Elo/Bradley-Terry

**Algorithm:** Elo rating system with per-sport feature augmentation.

**How it works:**
1. Maintain Elo ratings per team from historical results
2. Convert Elo difference to win probability: P(A wins) = 1 / (1 + 10^((Elo_B - Elo_A) / 400))
3. Adjust for home advantage, rest days, injury impact
4. Apply isotonic calibration on historical predictions vs outcomes

**Training data:** Team/player statistics from publicly available sports databases.

**Retraining schedule:** After each game day.

### 2.4 Event Intelligence — Cross-Category Event Router

Not an LLM-based "agent." A rule-based routing system that detects when a real-world event is relevant to multiple market categories.

```python
# event_router.py

EVENT_ROUTES = {
    # Weather events affecting multiple categories
    "hurricane_formation":    ["weather.hurricane", "econ.insurance", "econ.gdp_impact"],
    "hurricane_upgrade":      ["weather.hurricane", "econ.insurance", "econ.gdp_impact"],
    "extreme_heat_wave":      ["weather.temperature", "econ.energy"],
    "winter_storm":           ["weather.temperature", "weather.precipitation", "econ.energy"],

    # Economic events affecting multiple categories
    "fed_rate_decision":      ["econ.rates", "crypto.btc_target", "crypto.eth_target"],
    "surprise_nfp":           ["econ.employment", "econ.gdp", "crypto.btc_target"],
    "cpi_surprise":           ["econ.inflation", "crypto.btc_target"],

    # Geopolitical events
    "conflict_escalation":    ["econ.oil", "econ.defense", "crypto.btc_target"],
}

class EventRouter:
    def __init__(self, nc):
        self.nc = nc
        self.routes = EVENT_ROUTES

    async def on_event(self, event_type: str, event_data: dict):
        if event_type in self.routes:
            for target in self.routes[event_type]:
                await self.nc.publish(
                    f"events.routed.{target}",
                    json.dumps({
                        "source_event": event_type,
                        "data": event_data,
                        "priority": "boosted",
                        "routed_at": datetime.utcnow().isoformat()
                    }).encode()
                )
```

Models subscribe to `events.routed.{their_category}.*` and re-evaluate relevant contracts when a boost arrives.

### 2.5 Execution Engine Integration

The existing Go execution infrastructure requires minimal changes. The primary integration point is a new `SignalFunc` implementation that reads from NATS instead of computing signals internally.

**New file:** `services/strategy/external_signal.go`

```go
package strategy

import (
    "encoding/json"
    "sync"

    "autonomy-platform/internal/models"
    "github.com/nats-io/nats.go"
)

// ExternalSignalSource represents a signal from the Python intelligence layer.
type ExternalSignalSource struct {
    MarketID     string  `json:"market_id"`
    Side         string  `json:"side"`
    Quantity     int     `json:"quantity"`
    PriceMicros  int64   `json:"price_micros"`
    Edge         float64 `json:"edge"`
    ModelProb    float64 `json:"model_prob"`
    MarketProb   float64 `json:"market_prob"`
    Uncertainty  float64 `json:"uncertainty"`
    ModelVersion string  `json:"model_version"`
    Reason       string  `json:"reason"`
}

// ExternalSignal creates a SignalFunc that consumes signals from NATS
// published by the Python signal aggregator.
func ExternalSignal(nc *nats.Conn, subject string) SignalFunc {
    var mu sync.Mutex
    var pending []Signal

    nc.Subscribe(subject, func(msg *nats.Msg) {
        var ext ExternalSignalSource
        if err := json.Unmarshal(msg.Data, &ext); err != nil {
            return
        }

        side := models.SideBuy
        if ext.Side == "sell" {
            side = models.SideSell
        }

        mu.Lock()
        pending = append(pending, Signal{
            MarketID:    ext.MarketID,
            Side:        side,
            Quantity:    int32(ext.Quantity),
            PriceMicros: ext.PriceMicros,
            Reason:      ext.Reason,
        })
        mu.Unlock()
    })

    return func(data map[string]*models.MarketData) []Signal {
        mu.Lock()
        signals := pending
        pending = nil
        mu.Unlock()
        return signals
    }
}
```

This slots into the existing `Engine.RunSignalLoop()` in `services/strategy/engine.go` without changes to the engine itself. The `SignalFunc` interface (`func(data map[string]*models.MarketData) []Signal`) remains unchanged.

### 2.6 Portfolio and Risk System Extensions

The existing 20-point risk check framework (`services/risk/checks.go`) handles per-trade validation. The following extensions add portfolio-level intelligence.

#### Per-Model Risk Attribution

Add to `risk.daily_stats`:
```sql
-- Migration: 004_model_attribution.up.sql
ALTER TABLE risk.daily_stats ADD COLUMN model_version TEXT;
```

The strategy_id field already segments by strategy. When multiple models publish signals, each should use a distinct strategy_id (e.g., `weather-emos-v3`, `econ-surprise-v1`, `crypto-iv-v2`). The existing per-strategy risk checks then apply independently per model.

#### Category Exposure Limits

Extend the policy YAML:
```yaml
per_category:
  weather:
    max_exposure_micros: 30000000000     # $30k
    max_daily_loss_micros: -10000000000  # -$10k
    max_concurrent_positions: 20
  economic:
    max_exposure_micros: 20000000000     # $20k
    max_daily_loss_micros: -7000000000   # -$7k
    max_concurrent_positions: 10
  crypto:
    max_exposure_micros: 15000000000     # $15k
    max_daily_loss_micros: -5000000000   # -$5k
    max_concurrent_positions: 5
```

Implementation: add a `checkCategoryExposure` function to `services/risk/checks.go` that maps strategy_id prefix to category and enforces category-level limits. This is a single new check added to the existing check chain.

#### Model Degradation Kill Switch

Add a new NATS subject: `calibration.brier.{model_id}`. The Python calibration tracker publishes rolling Brier scores. A Go handler in the watchdog service subscribes and triggers `soft_pause` for the specific strategy if Brier score exceeds threshold.

```go
// Addition to services/watchdog/killswitch.go
func (ks *KillSwitch) MonitorCalibration(nc *nats.Conn) {
    nc.Subscribe("calibration.brier.*", func(msg *nats.Msg) {
        var report struct {
            ModelID    string  `json:"model_id"`
            BrierScore float64 `json:"brier_score"`
            Baseline   float64 `json:"baseline"`
            WindowDays int     `json:"window_days"`
        }
        if err := json.Unmarshal(msg.Data, &report); err != nil {
            return
        }
        if report.BrierScore > report.Baseline*1.15 {
            ks.Trigger(context.Background(), TriggerRequest{
                Level:       LevelSoftPause,
                Scope:       "strategy:" + report.ModelID,
                Reason:      fmt.Sprintf("calibration degraded: brier=%.4f baseline=%.4f", report.BrierScore, report.Baseline),
                TriggeredBy: "calibration_monitor",
            })
        }
    })
}
```

### 2.7 Backtesting Engine

The backtesting engine is a Python-only component that replays historical data through the forecasting models and computes simulated P&L.

```
backtesting/
├── __init__.py
├── backtest_runner.py      # Main backtesting harness
├── data_loader.py          # Load Parquet files into memory
├── simulated_market.py     # Simulated orderbook from historical snapshots
├── metrics.py              # Sharpe, max drawdown, Brier score, profit factor
└── reports/                # HTML/JSON backtest reports
```

**Critical requirement:** The backtester must use the same model code as the live system. Models are imported from `collectors/{category}/model.py`, not reimplemented. This prevents backtest/live divergence.

**Backtesting does NOT flow through the Go infrastructure.** Risk checks and execution are simulated in Python using simplified fill assumptions. The purpose of backtesting is to validate model accuracy and edge, not to test the execution pipeline (that's what integration tests are for).

```python
class BacktestRunner:
    def __init__(self, model: ForecastModel, start_date: str, end_date: str):
        self.model = model
        self.data = DataLoader(start_date, end_date)
        self.trades = []
        self.predictions = []

    def run(self):
        for date in self.data.dates():
            features = self.data.features_at(date)
            contracts = self.data.contracts_at(date)  # historical Kalshi contracts

            for contract in contracts:
                forecast = self.model.forecast(contract, features)
                self.predictions.append((forecast.model_prob, contract.resolved_yes))

                # Simulate trade decision
                market_mid = (contract.bid + contract.ask) / 2
                edge = self._compute_edge(forecast, contract)
                if edge > 0.05:
                    self.trades.append(SimulatedTrade(
                        date=date,
                        contract=contract,
                        model_prob=forecast.model_prob,
                        market_prob=market_mid,
                        edge=edge,
                        pnl=self._compute_pnl(forecast, contract),
                    ))

    def report(self) -> BacktestReport:
        return BacktestReport(
            brier_score=brier_score(self.predictions),
            total_pnl=sum(t.pnl for t in self.trades),
            sharpe=sharpe_ratio(self.trades),
            max_drawdown=max_drawdown(self.trades),
            num_trades=len(self.trades),
            win_rate=win_rate(self.trades),
            avg_edge=mean(t.edge for t in self.trades),
        )
```

---

## Section 3 — Phase-by-Phase Build Plan

### Phase 1 — Backtest Validation and Market Sizing

**Objective:** Answer two questions before building anything: (1) Does a calibrated weather model have edge over Kalshi market prices? (2) Is there enough liquidity to trade profitably?

**Systems built:**
- Historical data downloader: GFS reforecast archive + NOAA observations (Python script)
- EMOS model training pipeline (Python script)
- Backtest harness with simulated P&L (Python)
- Kalshi market data logger: fetch and store live orderbook snapshots (Python script)

**APIs required:**
- NOAA NOMADS (GFS reforecast data): free, HTTPS, no auth
- Open-Meteo (ECMWF historical): free, HTTPS, no auth
- NOAA ISD (station observations): free, HTTPS, no auth
- Kalshi API (read-only, market data): requires account, API key

**Database design:**
```sql
-- New migration: 004_market_data_logging.up.sql
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
```

**Tech stack:**
- Python 3.11+, numpy, scipy, scikit-learn, pandas, pyarrow, requests
- Existing Postgres (add market_data schema)
- Parquet files for bulk historical data

**Latency considerations:** None. This is batch research, not live trading.

**Testing approach:**
- Brier score < 0.20 on out-of-sample weather forecasts (2024-2025 holdout)
- Reliability diagram shows calibration near diagonal
- Simulated Sharpe ratio > 1.0 after fees
- Positive simulated P&L on 60%+ of months
- Orderbook logging validates: ≥20 active weather contracts on Kalshi with ≥$100 depth

**Deployment plan:** Run locally. No infrastructure changes.

**Engineering risks:**
- GFS reforecast data may be difficult to parse (GRIB2 format requires eccodes library)
- Kalshi API rate limits (10 req/sec) may slow orderbook collection
- Historical Kalshi orderbook data may not be publicly available, limiting backtest accuracy

**Success criteria:**
- **GO:** Brier score < 0.20, simulated monthly Sharpe > 1.0, ≥20 tradeable weather contracts with ≥$100 depth
- **NO-GO:** Brier score > 0.25, or simulated edge < 3 cents after fees, or <10 contracts with <$50 depth. If no-go, evaluate economic indicators as alternate first vertical before proceeding.

**Exit gate: Do not proceed to Phase 2 unless success criteria are met.**

---

### Phase 2 — Live Weather Model and Kalshi Integration

**Objective:** Deploy the weather model in paper trading mode against real Kalshi market data. Simultaneously build the Kalshi API adapter for order submission.

**Systems built:**
- `weather_brain.py`: single Python process — data ingestion, model inference, edge calculation, signal publication
- Kalshi REST API client: `pkg/kalshi/client.go`
- Kalshi market data feed: `services/data/kalshi_feed.go`
- `ExternalSignal` strategy function: `services/strategy/external_signal.go`
- Kalshi venue adapter: `pkg/kalshi/adapter.go` implementing `VenueAdapter` interface
- Orderbook snapshot persistence (Postgres)
- Prediction logging for calibration tracking

**APIs required:**
- Kalshi REST API: market data (GET /markets, GET /markets/{ticker}/orderbook)
- Kalshi REST API: order submission (POST /orders — initially paper mode only)
- NOAA NOMADS: GFS ensemble data (HTTPS)
- Open-Meteo: ECMWF data (HTTPS)
- NOAA ISD: station observations (HTTPS)

**Database design:**
```sql
-- New migration: 005_prediction_tracking.up.sql
CREATE SCHEMA IF NOT EXISTS models;

CREATE TABLE models.predictions (
    id              BIGSERIAL PRIMARY KEY,
    model_id        TEXT NOT NULL,          -- e.g., "weather-emos-v1"
    market_id       TEXT NOT NULL,
    venue           TEXT NOT NULL,
    model_prob      DOUBLE PRECISION NOT NULL,
    uncertainty     DOUBLE PRECISION,
    market_mid      DOUBLE PRECISION,
    edge            DOUBLE PRECISION,
    raw_prob        DOUBLE PRECISION,
    features_json   JSONB,
    predicted_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE models.outcomes (
    id              BIGSERIAL PRIMARY KEY,
    market_id       TEXT NOT NULL,
    venue           TEXT NOT NULL,
    resolved_yes    BOOLEAN NOT NULL,
    resolved_at     TIMESTAMPTZ NOT NULL,
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE models.calibration_scores (
    id              BIGSERIAL PRIMARY KEY,
    model_id        TEXT NOT NULL,
    brier_score     DOUBLE PRECISION NOT NULL,
    num_predictions INTEGER NOT NULL,
    window_start    TIMESTAMPTZ NOT NULL,
    window_end      TIMESTAMPTZ NOT NULL,
    computed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_pred_model_time ON models.predictions(model_id, predicted_at);
CREATE INDEX idx_outcomes_market ON models.outcomes(market_id, resolved_at);
CREATE INDEX idx_cal_model_time ON models.calibration_scores(model_id, computed_at);
```

**Tech stack:**
- Python: nats-py, psycopg2, requests, schedule, scipy, scikit-learn, eccodes
- Go: existing stack + `pkg/kalshi/` (new HTTP client for Kalshi REST API)
- NATS subject: `signals.weather` (weather model → strategy engine)

**Kalshi adapter implementation:**

The adapter implements the existing `VenueAdapter` interface in `services/execution/engine.go`:

```go
// pkg/kalshi/adapter.go
package kalshi

type Adapter struct {
    client   *Client           // Kalshi REST API client
    paperMode bool             // true = log but don't submit
}

func (a *Adapter) SubmitOrder(ctx context.Context, order *models.ProposedOrder, internalID string) (*execution.ExchangeAck, error) {
    // Map internal order to Kalshi API format
    // POST /portfolio/orders
    // Return exchange order ID
}

func (a *Adapter) CancelOrder(ctx context.Context, exchangeOrderID string) error {
    // DELETE /portfolio/orders/{order_id}
}

func (a *Adapter) CancelAll(ctx context.Context) (int, error) {
    // DELETE /portfolio/orders (cancel all)
}

func (a *Adapter) PollFills(ctx context.Context, since time.Time) ([]execution.ExchangeFill, error) {
    // GET /portfolio/fills?min_ts=since
}

func (a *Adapter) GetOrderStatus(ctx context.Context, exchangeOrderID string) (*execution.ExchangeOrderStatus, error) {
    // GET /portfolio/orders/{order_id}
}
```

**Latency considerations:**
- Weather model cycle: 60 seconds (not latency-sensitive)
- Kalshi API round-trip: 100-200ms (not in our control)
- NATS signal delivery: <1ms (local)
- Total signal-to-order: <500ms (acceptable for weather)

**Testing approach:**
- Integration test: weather_brain.py → NATS → ExternalSignal → risk engine → execution engine → Kalshi adapter (paper mode)
- Verify NATS message schema compatibility between Python and Go
- Paper trading for 2+ weeks with real Kalshi market data
- Compare paper P&L against backtest expectations
- Validate that Kalshi adapter correctly implements VenueAdapter interface (use existing execution engine tests with Kalshi adapter swapped in)

**Deployment plan:**
- Add `weather-model` Python service to docker-compose.yml
- Add Kalshi API credentials to environment (read-only API key initially)
- Update strategy-engine configuration: `STRATEGY=external-signal`, `SIGNAL_NATS_SUBJECT=signals.weather`
- Run in paper mode: Kalshi adapter in paperMode=true (logs orders, does not submit)

**Engineering risks:**
- Kalshi API authentication may require periodic token refresh
- Kalshi market ID format may differ from internal format — need mapping layer
- GRIB2 parsing can fail on corrupted downloads — need retry logic
- Python process crashes are independent of Go process health — need heartbeat from Python → watchdog

**Success criteria:**
- End-to-end signal flow from weather data → model → NATS → Go execution (paper mode)
- Paper trading matches backtest expectations within 20%
- Kalshi adapter passes all existing VenueAdapter integration tests
- Python process uptime >99% over 2-week test period
- Prediction logging operational with >95% coverage

---

### Phase 3 — Real Money Weather Trading

**Objective:** Go live with real money on Kalshi weather contracts at minimal capital ($500-1000).

**Systems built:**
- Kalshi adapter: switch from paper to live mode
- Production risk policy for weather (tightened limits)
- Calibration monitoring with automated kill switch
- Model degradation watchdog trigger
- Position sizing connected to live calibration confidence

**APIs required:**
- Kalshi trading API (write permissions, requires funding)

**Database design:** No new tables. Use existing schema.

**Tech stack:** No new technologies.

**Policy configuration:**
```yaml
# policies/weather_live.yaml
mode: live

execution:
  transmit_orders: true
  require_signed_approval: true
  require_heartbeat: true

allowed_markets:
  kalshi:
    - "KXTEMP-*"     # Temperature contracts
    - "KXPRECIP-*"   # Precipitation contracts

per_trade:
  max_notional_micros: 500000000      # $500
  max_quantity: 5
  max_spread_bps: 500

per_position:
  max_notional_micros: 2000000000     # $2,000
  max_concentration_pct: 15.0

per_strategy:
  max_daily_loss_micros: -1000000000  # -$1,000
  max_orders_per_minute: 5
  max_consecutive_losses: 5
  max_open_orders: 10

per_venue:
  kalshi:
    max_exposure_micros: 5000000000   # $5,000
    max_daily_loss_micros: -2000000000 # -$2,000

global:
  max_total_exposure_micros: 5000000000  # $5,000
  max_daily_loss_micros: -2000000000     # -$2,000
  max_drawdown_from_peak_pct: 10.0

data_quality:
  max_data_age_seconds: 300             # 5 min (weather is slow-moving)
  min_orderbook_depth: 1

kill_switch_triggers:
  global_daily_loss_micros: 2000000000
  global_drawdown_pct: 10.0
  strategy_daily_loss_micros: 1000000000
  strategy_consecutive_losses: 5
  heartbeat_timeout_seconds: 60
```

**Latency considerations:** Same as Phase 2. Weather signals are not time-sensitive.

**Testing approach:**
- Start with max 3 trades per day, max $100 per trade
- Manual review of every trade for first week
- Automated P&L tracking vs model predictions
- Weekly calibration review
- Kill switch drill: verify manual halt works in <30 seconds

**Deployment plan:**
- Kalshi adapter: `paperMode: false`
- Fund Kalshi account with $500-1000
- Deploy with tightened policy
- Operator must review `trade-ctl status` daily

**Engineering risks:**
- Kalshi API may have undocumented rate limits or order validation rules
- Real fill rates may differ from paper trading assumptions
- Slippage on thin markets may exceed backtest estimates

**Success criteria:**
- 30 days of live trading without system failures
- Realized P&L positive (any amount)
- Brier score within 10% of backtest baseline
- No kill switch triggers from bugs (legitimate risk triggers are OK)

---

### Phase 4 — Economic and Crypto Verticals

**Objective:** Add economic indicator and crypto price target models. Deploy signal aggregator for multi-model capital allocation.

**Systems built:**
- `econ_model.py`: economic indicator forecasting process
- `crypto_model.py`: crypto price target forecasting process
- `signal_aggregator.py`: central signal ranking and capital allocation
- Economic data collectors (FRED, BLS, consensus)
- Crypto data collectors (CEX WebSocket, Deribit IV, on-chain)
- Extended risk policy with per-category limits

**APIs required:**
- FRED API (free, requires key): economic time series
- BLS Public Data API (free): employment releases
- FocusEconomics or similar (free tier): consensus estimates
- Binance WebSocket (free): crypto prices
- Deribit API (free): options chain, implied volatility
- Glassnode (free tier): on-chain metrics

**Database design:**
```sql
-- Migration: 006_multi_model_support.up.sql

-- Index predictions by model for per-model P&L tracking
CREATE INDEX idx_pred_model_market ON models.predictions(model_id, market_id);

-- Category mapping for per-category risk limits
CREATE TABLE risk.strategy_categories (
    strategy_id     TEXT PRIMARY KEY,
    category        TEXT NOT NULL,           -- "weather", "economic", "crypto"
    model_version   TEXT,
    active          BOOLEAN DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

**Signal aggregator design:**

```python
# signal_aggregator.py

class SignalAggregator:
    """
    Consumes signals from all category models.
    Ranks by fee-adjusted edge.
    Applies capital allocation constraints.
    Publishes final sized signals.
    """

    def __init__(self, nc, config):
        self.nc = nc
        self.config = config
        self.pending_signals = []
        self.portfolio_state = PortfolioState()

    async def on_signal(self, msg):
        signal = json.loads(msg.data)
        self.pending_signals.append(signal)

    async def allocate_and_publish(self):
        """Called every N seconds or when signals accumulate."""
        if not self.pending_signals:
            return

        # 1. Rank by net edge
        ranked = sorted(self.pending_signals, key=lambda s: s["edge"], reverse=True)

        # 2. Apply capital constraints
        remaining_capital = self.portfolio_state.available_capital()
        category_budgets = self.config.category_budgets.copy()

        approved = []
        for signal in ranked:
            category = signal["category"]
            notional = signal["quantity"] * signal["price_micros"]

            if notional > remaining_capital:
                continue
            if notional > category_budgets.get(category, 0):
                continue

            # Check for correlation with existing positions
            correlation_penalty = self.compute_correlation_penalty(signal)
            if correlation_penalty > 0.5:
                signal["quantity"] = int(signal["quantity"] * (1 - correlation_penalty))

            if signal["quantity"] > 0:
                approved.append(signal)
                remaining_capital -= notional
                category_budgets[category] -= notional

        # 3. Publish approved signals
        for signal in approved:
            await self.nc.publish(
                "signals.aggregated",
                json.dumps(signal).encode()
            )

        self.pending_signals.clear()
```

**NATS subjects (new):**
- `signals.weather` → signal_aggregator (from weather model)
- `signals.economic` → signal_aggregator (from econ model)
- `signals.crypto` → signal_aggregator (from crypto model)
- `signals.aggregated` → Go strategy engine (from signal aggregator)

**Tech stack:**
- Python: same stack + websocket-client (for Binance), aiohttp
- Go: update ExternalSignal to consume `signals.aggregated` instead of `signals.weather`

**Latency considerations:**
- Economic model: must process releases within 60 seconds of publication. Use BLS release schedule to pre-arm the model.
- Crypto model: 10-second polling cycle. Not sub-second — edge persists for minutes on Kalshi contracts.
- Signal aggregator: <100ms from signal received to signal published. Pure computation.

**Testing approach:**
- Backtest each new model independently (same approach as Phase 1)
- Paper trade each model for 2 weeks before going live
- Validate signal aggregator correctly ranks and sizes across categories
- Integration test: 3 models → aggregator → NATS → Go pipeline → Kalshi adapter (paper mode)

**Deployment plan:**
- Add econ_model, crypto_model, signal_aggregator to docker-compose.yml
- Update strategy engine to consume `signals.aggregated`
- Deploy economic and crypto models in paper mode initially
- Switch to live after 2 weeks of paper validation per model

**Engineering risks:**
- Crypto WebSocket feeds may disconnect unexpectedly — need auto-reconnect
- Economic release timing may not be precise — BLS sometimes publishes seconds early/late
- Signal aggregator is a single point of failure — if it crashes, all signals stop. Need health check + auto-restart.

**Success criteria:**
- 3 models running concurrently with independent P&L tracking
- Signal aggregator correctly prioritizes higher-edge signals
- Portfolio-level Sharpe > individual model Sharpe (diversification benefit measured)
- Each model maintains Brier score within 15% of backtest baseline

---

### Phase 5 — Cross-Venue Expansion (Polymarket)

**Objective:** Add Polymarket as a second trading venue. Enable trading on contracts that exist on Polymarket but not Kalshi, and vice versa.

**Systems built:**
- Polymarket CLOB API client: `pkg/polymarket/client.go`
- Polymarket venue adapter: `pkg/polymarket/adapter.go`
- Polygon wallet integration for on-chain settlement
- Cross-venue position tracking in reconciliation engine
- Polymarket orderbook logger

**APIs required:**
- Polymarket CLOB API: orderbook, order submission
- Polygon RPC: on-chain transaction submission
- Polymarket subgraph: position tracking (GraphQL)

**Database design:**
```sql
-- Migration: 007_polymarket_venue.up.sql

-- Track Polymarket-specific order metadata
ALTER TABLE execution.orders ADD COLUMN chain_tx_hash TEXT;
ALTER TABLE execution.orders ADD COLUMN gas_cost_micros BIGINT DEFAULT 0;

-- Cross-venue position netting
CREATE TABLE risk.cross_venue_positions (
    event_id        TEXT NOT NULL,          -- Shared event identifier
    kalshi_market   TEXT,
    poly_market     TEXT,
    kalshi_position INTEGER DEFAULT 0,
    poly_position   INTEGER DEFAULT 0,
    net_exposure    BIGINT DEFAULT 0,       -- micros
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (event_id)
);
```

**Polymarket adapter implementation:**

```go
// pkg/polymarket/adapter.go
package polymarket

type Adapter struct {
    client      *CLOBClient    // Polymarket CLOB API
    wallet      *Wallet        // Polygon wallet for signing
    chainClient *ethclient.Client  // Polygon RPC
}

func (a *Adapter) SubmitOrder(ctx context.Context, order *models.ProposedOrder, internalID string) (*execution.ExchangeAck, error) {
    // 1. Sign order with EIP-712
    // 2. Submit to Polymarket CLOB API
    // 3. Wait for on-chain confirmation (or CLOB fill)
    // 4. Return exchange ack
}

// Note: gas cost tracking is critical — deducted from edge calculation
```

**Latency considerations:**
- Polymarket CLOB: 200-500ms for order submission
- Polygon on-chain settlement: 2-5 seconds (block time)
- Gas cost: varies, must be included in fee calculation by Python models
- Cross-venue reconciliation: run every 60 seconds

**Testing approach:**
- Deploy on Polygon Mumbai testnet first (no real funds)
- Validate adapter against all VenueAdapter interface tests
- Paper trade on Polymarket for 2 weeks
- Reconciliation test: simulate position mismatch and verify kill switch triggers

**Engineering risks:**
- Polygon gas price spikes can make small trades unprofitable
- On-chain transactions are irreversible — bugs in the adapter can lose funds
- Polymarket API may change without notice (less stable than Kalshi)
- Wallet private key management is a critical security concern — use KMS or hardware wallet

**Success criteria:**
- Polymarket adapter passes all VenueAdapter interface tests
- Cross-venue position tracking accurate to the contract
- Gas costs correctly deducted from P&L calculations
- 2 weeks of paper trading without adapter failures

---

### Phase 6 — Portfolio Optimization and Risk Enhancement

**Objective:** Implement portfolio-level capital allocation, correlation risk groups, and performance-based rebalancing.

**Systems built:**
- Capital allocation optimizer (Python, within signal_aggregator)
- Correlation risk groups (Go, within risk engine)
- Performance-based strategy rebalancing
- Graduated drawdown tiers (Go, within watchdog)
- Per-model P&L dashboard data (Postgres views)

**Database design:**
```sql
-- Migration: 008_portfolio_management.up.sql

CREATE TABLE risk.correlation_groups (
    group_name          TEXT NOT NULL,
    market_id           TEXT NOT NULL,
    venue               TEXT NOT NULL,
    max_group_exposure  BIGINT NOT NULL,    -- micros
    PRIMARY KEY (group_name, market_id, venue)
);

CREATE TABLE risk.strategy_performance (
    strategy_id         TEXT NOT NULL,
    date                DATE NOT NULL,
    sharpe_30d          DOUBLE PRECISION,
    max_drawdown_30d    DOUBLE PRECISION,
    win_rate_30d        DOUBLE PRECISION,
    brier_score_30d     DOUBLE PRECISION,
    allocation_weight   DOUBLE PRECISION,   -- current allocation fraction
    PRIMARY KEY (strategy_id, date)
);

-- Graduated drawdown response
CREATE TABLE watchdog.drawdown_tiers (
    tier_id             SERIAL PRIMARY KEY,
    threshold_pct       DOUBLE PRECISION NOT NULL,
    action              TEXT NOT NULL,       -- "reduce_sizing_50pct", "new_positions_only", "soft_pause", "full_stop"
    active              BOOLEAN DEFAULT true
);

INSERT INTO watchdog.drawdown_tiers (threshold_pct, action) VALUES
    (2.0, 'reduce_sizing_50pct'),
    (3.5, 'new_positions_only'),
    (5.0, 'soft_pause'),
    (7.0, 'full_stop');
```

**Correlation risk implementation:**

```go
// Addition to services/risk/checks.go

func checkCorrelationGroup(state *State, order *models.ProposedOrder, policy *config.Policy) CheckResult {
    // Look up which correlation groups this market belongs to
    groups := state.CorrelationGroups[order.MarketID]
    for _, group := range groups {
        groupExposure := int64(0)
        for _, marketID := range group.MarketIDs {
            if pos, ok := state.Markets[marketID]; ok {
                groupExposure += abs(pos.Notional)
            }
        }
        groupExposure += order.NotionalMicros

        if groupExposure > group.MaxGroupExposure {
            return CheckResult{
                Passed: false,
                Reason: fmt.Sprintf("correlation group %s exposure %d exceeds limit %d",
                    group.Name, groupExposure, group.MaxGroupExposure),
            }
        }
    }
    return CheckResult{Passed: true}
}
```

**Latency considerations:** None of these are latency-sensitive. Capital allocation runs on a 60-second cycle. Correlation checks add <1ms to the existing risk check chain.

**Testing approach:**
- Unit test correlation group checks with multi-contract scenarios
- Backtest capital allocator: verify Sharpe improvement over equal-weight
- Integration test graduated drawdown: simulate losses and verify each tier triggers correctly
- Paper trade with allocator for 2 weeks

**Success criteria:**
- Portfolio Sharpe ratio > max individual strategy Sharpe (diversification benefit)
- Correlation groups prevent >50% exposure to any single underlying event
- Graduated drawdown tiers trigger correctly at each threshold
- Capital allocator shifts >20% of capital from worst to best performer over 30 days

---

### Phase 7 — Sports Vertical and Alpha Research

**Objective:** Add sports model. Build research tooling for rapid development of new verticals.

**Systems built:**
- `sports_model.py`: Elo/Bradley-Terry model with feature augmentation
- Sports data collectors (odds APIs, team statistics)
- Cross-category event router
- Research notebook templates for new vertical development
- Automated backtest pipeline (CLI: `python -m backtesting.run --model=sports --start=2024-01-01`)

**APIs required:**
- The Odds API (free tier): live odds from multiple bookmakers
- Sports reference sites (web scrape or API): team/player stats
- Weather API (for outdoor sports game conditions)

**Tech stack:** Same stack. No new infrastructure.

**Testing approach:**
- Backtest Elo model on 2023-2025 sports seasons
- Validate calibration on held-out 2025 season
- Paper trade for 4 weeks (sports seasons are long, need more data)

**Success criteria:**
- Elo model Brier score < 0.22 on out-of-sample data
- Sports vertical adds >$100/month incremental profit
- Event router correctly boosts signals when weather affects outdoor sports contracts

---

### Phase 8 — Production Hardening and Advanced Features

**Objective:** Make the system production-grade for sustained operation with >$50k deployed capital.

**Systems built:**
- Prometheus metrics exporter per service (Go + Python)
- Grafana dashboards: per-model P&L, calibration, latency, system health
- mTLS between Go services (TLS certificates)
- Vault for credential management (API keys, HMAC keys, wallet keys)
- Automated model retraining pipeline (cron-based, per model schedule)
- News/LLM extraction layer (for news-driven markets)
- Web dashboard for real-time monitoring

**Tech stack additions:**
- Prometheus + Grafana (Docker containers)
- HashiCorp Vault (Docker container or managed service)
- OpenAI API or local LLM (for news text extraction only)

**Deployment plan:**
- Migrate from docker-compose to Docker Swarm or single-node Kubernetes
- Automated health checks for all Python processes
- Log aggregation (Loki or similar)
- Alerting: PagerDuty or Telegram for kill switch events and calibration drift

**Success criteria:**
- System runs unattended for 7+ days without intervention
- Mean time to detect model degradation < 24 hours
- All credentials in Vault, none in environment variables
- Dashboards provide full operational visibility

---

## Section 4 — Recommended Tech Stack

### Core Stack (Justified)

| Technology | Component | Why This Choice |
|-----------|-----------|-----------------|
| **Go 1.23** | Execution, risk, reconciliation, venue adapters | Already built. Type-safe, fast, excellent for network services and state machines. The existing 20-point risk check + HMAC + intent ledger is production-quality Go. No reason to change. |
| **Python 3.11+** | Data ingestion, models, backtesting, signal aggregation | Entire data science ecosystem (scipy, sklearn, pandas, xarray, eccodes). Not latency-critical. Python's weakness (speed) is irrelevant when your model runs every 60 seconds. |
| **PostgreSQL 16** | Primary data store | Already in stack. Handles orders, risk state, audit log, predictions, calibration scores. JSONB for flexible payloads. Rock-solid for financial data. No need for a second database. |
| **NATS JetStream** | Event bus, inter-service messaging | Already in stack. Handles all pub/sub between Python and Go processes. JetStream provides persistence, replay, and exactly-once delivery. Eliminates need for Kafka/Redpanda at this scale. |
| **Docker + docker-compose** | Deployment | Already in stack. Sufficient until >$100k deployed capital. Kubernetes is negative ROI before that. |

### Additional Stack (Phase-Specific)

| Technology | When Added | Why |
|-----------|-----------|-----|
| **Parquet files** | Phase 1 | Bulk historical data for backtesting. Columnar format is 10x faster than CSV for numerical data. Used by pandas/pyarrow natively. No infrastructure — just files on disk. |
| **eccodes / cfgrib** | Phase 1 | GFS GRIB2 weather data parsing. Only option for reading NWP model output. Python library, C backend. |
| **Prometheus** | Phase 8 | Metrics collection. Industry standard, integrates with Go (prometheus/client_golang) and Python (prometheus_client). |
| **Grafana** | Phase 8 | Dashboarding. Reads from Prometheus. Free, self-hosted. |
| **HashiCorp Vault** | Phase 8 | Secret management for API keys, HMAC keys, wallet private keys. Critical when trading real money across multiple venues. |

### Explicitly Rejected Technologies

| Technology | Why Rejected |
|-----------|-------------|
| **Kafka / Redpanda** | NATS JetStream handles throughput fine. You're processing hundreds of messages/second, not millions. Kafka adds operational complexity (ZooKeeper/KRaft, partition management) with zero benefit at this scale. |
| **Redis** | No shared feature store needed. Each model maintains in-memory state. Postgres handles all persistent lookups. Adding Redis means another process to monitor, another failure mode, another data consistency concern. |
| **ClickHouse** | Postgres with proper indexes handles all analytical queries. You're querying thousands of rows, not billions. TimescaleDB extension is an option later if time-series queries become slow, but unlikely at this scale. |
| **Rust** | Go is fast enough for the execution path (sub-millisecond for risk checks). Python is fine for modeling (60-second signal loop). A third language adds cognitive overhead, build complexity, and hiring difficulty with no measurable performance gain. |
| **Vector database (Pinecone, Qdrant, etc.)** | All data is structured and numerical. Vector similarity search solves a problem that doesn't exist in this system. |
| **LangChain / CrewAI / agent frameworks** | The "multi-agent" architecture is multiple independent processes communicating via NATS, not LLM-based agents. Agent frameworks add non-determinism, latency, and debugging complexity. The event router is 50 lines of Python, not a prompt chain. |
| **Kubernetes** | Negative ROI until >$100k deployed capital and >99.9% uptime requirement. Docker-compose with health checks and auto-restart is sufficient. A single-node system with 12 processes does not need container orchestration. |
| **Airflow / Kubeflow / MLflow** | Overkill for 4-5 models retrained on simple schedules. Cron jobs are sufficient. MLOps infrastructure pays for itself at 20+ models with complex DAG dependencies. |

---

## Section 5 — 90-Day Execution Plan

### Prerequisites (Before Day 1)

- [ ] Kalshi account created with API access
- [ ] Kalshi read-only API key obtained
- [ ] Python environment set up (3.11+, virtualenv)
- [ ] eccodes library installed (for GRIB2 parsing)
- [ ] Existing system running in docker-compose (Phase 9 verified)

### Week 1-2: Backtest Validation (Phase 1)

| Day | Task | Deliverable |
|-----|------|-------------|
| 1-2 | Download GFS reforecast data (2020-2025) | `data/backtest/weather/gfs_reforecast.parquet` |
| 3 | Download NOAA station observations | `data/backtest/weather/noaa_obs.parquet` |
| 4-5 | Download ECMWF historical via Open-Meteo | `data/backtest/weather/ecmwf_historical.parquet` |
| 6-7 | Train EMOS model on 2020-2023 data | `models/weather_emos_v1.pkl` |
| 8-9 | Run backtest on 2024-2025 holdout | Brier score, reliability diagram, simulated P&L |
| 10 | **Decision gate: GO/NO-GO on weather vertical** | Report with Brier score, Sharpe, edge distribution |
| 11-12 | Build Kalshi orderbook logger | `market_data.orderbook_snapshots` populated |
| 13-14 | Analyze Kalshi weather market liquidity | Report: contract count, depth, spread distribution |

**Milestone: Validated model edge + market liquidity assessment**

### Week 3-4: Live Integration (Phase 2)

| Day | Task | Deliverable |
|-----|------|-------------|
| 15-17 | Build `weather_brain.py` (fetch + model + signal publish) | Running Python process publishing to NATS |
| 18-19 | Build Kalshi REST API client (`pkg/kalshi/client.go`) | Go client with auth, rate limiting, error handling |
| 20-21 | Build Kalshi venue adapter (`pkg/kalshi/adapter.go`) | Implements VenueAdapter interface |
| 22 | Build `ExternalSignal` strategy func | `services/strategy/external_signal.go` |
| 23-24 | Integration: end-to-end paper trading | Full signal flow: Python → NATS → Go → Kalshi (paper) |
| 25 | Database migration for prediction tracking | `models.predictions`, `models.outcomes`, `models.calibration_scores` |
| 26-28 | Paper trading validation (runs continuously) | 3 days of paper results |

**Milestone: End-to-end system running with real Kalshi data in paper mode**

### Week 5-6: Paper Validation + Real Money Preparation (Phase 2 → 3)

| Day | Task | Deliverable |
|-----|------|-------------|
| 29-42 | Continue paper trading (14 days) | Paper P&L report, comparison to backtest |
| 35 | Build calibration monitoring | Brier score tracking, kill switch trigger |
| 37 | Write production risk policy | `policies/weather_live.yaml` |
| 39 | Kalshi account funding + trading API key | Funded account with write permissions |
| 40 | Deploy kill switch drill | Verify manual halt <30 seconds |
| 41-42 | Review paper results, final go/no-go for real money | Decision document |

**Milestone: 2 weeks of paper trading validated, ready for real money**

### Week 7-8: Real Money Weather (Phase 3)

| Day | Task | Deliverable |
|-----|------|-------------|
| 43 | Go live with $500, max 3 trades/day | First real trade |
| 43-56 | Live trading with daily monitoring | Daily P&L log, calibration checks |
| 49 | Begin economic model backtest (parallel) | Econ backtest setup |
| 51 | Begin crypto model backtest (parallel) | Crypto backtest setup |
| 56 | 2-week live trading review | P&L report, model performance assessment |

**Milestone: Real money trading operational, second/third verticals in research**

### Week 9-10: Economic + Crypto Models (Phase 4)

| Day | Task | Deliverable |
|-----|------|-------------|
| 57-60 | Complete economic model training + backtest | Econ model validated |
| 61-63 | Complete crypto model training + backtest | Crypto model validated |
| 64-66 | Build signal aggregator | `signal_aggregator.py` operational |
| 67-68 | Build econ + crypto data collectors | `econ_pod.py`, `crypto_pod.py` |
| 69-70 | Integration: 3 models → aggregator → execution | End-to-end with 3 models in paper mode |

**Milestone: Multi-model system running in paper mode**

### Week 11-12: Multi-Model Live + Portfolio (Phase 4 → 6)

| Day | Task | Deliverable |
|-----|------|-------------|
| 71-77 | Paper trade econ + crypto models (7 days) | Paper P&L per model |
| 78 | Go live with econ + crypto (small size) | 3 models live |
| 79-80 | Build per-category risk limits | Extended policy YAML |
| 81-82 | Build correlation risk groups | `checkCorrelationGroup` in risk engine |
| 83-84 | Review full portfolio performance | Portfolio-level Sharpe, per-model attribution |
| 85-90 | Optimize, tune, iterate | Adjusted edge thresholds, position sizing |

**Milestone: Multi-vertical trading system operational with portfolio-level risk management**

### 90-Day Expected State

By day 90, the following should be operational:

- 3 forecasting models (weather, economic, crypto) running live
- Signal aggregator with capital allocation
- Kalshi venue adapter (live)
- Per-model risk attribution and category limits
- Calibration monitoring with automated kill switch
- Correlation risk groups for related contracts
- $2,000-$10,000 deployed capital (scaled based on results)
- Prediction logging and calibration tracking
- Daily P&L reporting per model

**What is NOT done by day 90:**
- Polymarket integration (Phase 5, month 4-5)
- Sports model (Phase 7, month 5-6)
- Cross-venue arbitrage (Phase 5+)
- Production hardening: Prometheus, Grafana, Vault (Phase 8, month 6+)
- News/LLM extraction (Phase 8)
- Web dashboard (Phase 8)

---

## Section 6 — Biggest Profit Killers

### 1. Trading Without Validated Edge

Building infrastructure before proving the model works. If the backtest shows Brier score > 0.25 or edge < 3 cents after fees, stop. Do not proceed. The most expensive mistake is deploying capital on a model that doesn't have edge — you'll attribute the losses to "bad luck" or "market conditions" for months before admitting the model is wrong.

**Mitigation:** Phase 1 is backtest-only. Hard go/no-go gate before any live infrastructure is built.

### 2. Ignoring Fees and Spread in Edge Calculation

A 5-cent model edge minus 3 cents of spread minus 1 cent of Kalshi fees = 1 cent of real edge. Many traders compute edge against mid price, which overstates profitability by 50-100%. The edge calculation MUST use the price you'll actually trade at (the ask for buys, the bid for sells), not the mid.

**Mitigation:** `net_edge = model_prob - market_ask - fee_rate` is enforced in the signal generation code. The `min_edge` threshold is applied after fee deduction.

### 3. Calibration Drift Going Undetected

A model that was calibrated in January may be miscalibrated by March due to seasonal effects, market participant changes, or data source modifications. If calibration degrades from Brier 0.18 to 0.28 and you don't notice for a month, you lose on every trade during that period.

**Mitigation:** Automated Brier score monitoring. Kill switch trigger at 15% degradation. Weekly manual review of reliability diagrams.

### 4. Correlated Position Blow-Ups

Five weather contracts about the same storm. Three crypto contracts about BTC hitting different price targets (all correlated with BTC price). If your model is wrong about the underlying driver, you lose on ALL correlated positions simultaneously. Without correlation groups, your per-position risk limits don't protect against this.

**Mitigation:** Correlation risk groups (Phase 6). Manual review of open positions for hidden correlations.

### 5. Overfitting the Backtest

Optimizing model hyperparameters on the same data you use to estimate P&L. If you try 50 model configurations and pick the best backtest, you've massively overstated expected performance. The model that looked best in-sample will underperform out-of-sample.

**Mitigation:** Strict train/test split. Train on 2020-2023, test on 2024-2025, never touch the test set during development. Use cross-validation within the training set for hyperparameter selection.

### 6. Building Infrastructure Instead of Researching Edge

Engineering time spent on Kubernetes, dashboards, and CI/CD pipelines is time not spent on finding profitable markets, improving models, and analyzing data. The system makes money from model accuracy, not from deployment elegance.

**Mitigation:** The build plan sequences research (Phases 1-4) before infrastructure hardening (Phase 8). Do not jump to Phase 8 early.

### 7. Not Cutting Losing Models

Sunk cost fallacy. You spent 80 hours building the crypto model but it's losing money. The temptation is to "tune it more" or "give it more time." In practice, if a model isn't profitable after 60 days of live trading with >50 trades, it probably doesn't have edge. Kill it and reallocate capital to models that work.

**Mitigation:** Per-model P&L tracking. 60-day review gate for each model. Hard rule: if cumulative P&L is negative after 60 days and >50 trades, halt and review.

---

## Section 7 — Hard Truths

### 1. Prediction Markets May Not Have Enough Liquidity for Meaningful Profit

This is the existential risk. Kalshi weather contracts might have $200-500 of orderbook depth per contract. If you can deploy $10k total across all weather contracts and earn 3% monthly, that's $300/month. After the engineering investment, that may not justify the time. The backtest in Phase 1 must quantify this. If the addressable opportunity is <$500/month, the engineering effort is better spent elsewhere.

### 2. Calibration Is the Whole Game — and It's Fragile

Every dollar of profit depends on the gap between your calibrated probability and the market price. A 3% calibration error can flip a profitable strategy into a losing one. Calibration degrades for reasons you can't predict: weather model upgrades by NOAA, changes in market participant behavior, shifts in which contracts Kalshi lists. You will spend more time maintaining calibration than you spent building the model.

### 3. Your Competitors Are Getting Smarter

Prediction markets are becoming more efficient. Early movers had 10+ cents of edge. As more quantitative participants enter, edge compresses. The weather model that works today may not work in 12 months because other participants deploy similar models. You need to continuously research new data sources and modeling approaches to maintain edge. This is not a "build it and forget it" system.

### 4. The Data Pipeline Is 60% of the Work

Parsing GRIB2 files from NOAA, handling missing GFS ensemble members, dealing with API rate limits, managing timezone conversions for economic releases, reconnecting dropped WebSocket feeds — this is where the engineering time goes. The model itself (EMOS, isotonic regression) is 50 lines of scipy. The data pipeline to feed it is 500 lines of error handling, retry logic, and format conversion.

### 5. Backtesting Prediction Markets Is Harder Than Backtesting Equities

You need historical orderbook data to know what prices you could have traded at. For equities, this data is cheap and comprehensive. For Kalshi, historical orderbook data may not be publicly available. Your backtest may use model accuracy (Brier score against outcomes) as a proxy for profitability, but model accuracy and trading profitability are not the same thing. A model can be well-calibrated but unprofitable if the spread is too wide.

### 6. Cross-Venue Arbitrage Is Mostly a Fantasy at Small Scale

Kalshi and Polymarket price the same events differently. The spread looks like free money. It's not. After Kalshi fees (1-7%), Polymarket gas costs ($0.50-5 per transaction), execution risk (one leg fills, the other doesn't), and the capital locked in two venues simultaneously, net arb profit is often negative. True arbitrage requires large capital, fast execution, and the ability to absorb one-leg risk. Build this only after proving directional forecasting works.

### 7. You Will Lose Money in the First Month

Even with a well-calibrated model, 30 days is not enough trades to overcome variance. A strategy with 55% win rate and 3-cent edge needs ~200 trades to be statistically distinguishable from chance. In the first month, you may have 30-50 trades, and the outcome is dominated by luck. Do not over-react to early losses. Do not over-react to early gains either. Evaluate after 60+ days and 100+ trades.

### 8. Engineering Complexity Compounds Faster Than You Expect

At Phase 4 (3 models + aggregator + venue adapter), you're running 10+ processes. Each has its own failure modes, configuration, and monitoring needs. A bug in the crypto collector that corrupts a NATS message can cause the signal aggregator to crash, which stops all signals, which means your weather model's valid trades don't execute. Distributed system debugging is hard. Keep the system as simple as possible for as long as possible.

---

*Document version: 1.0*
*Last updated: 2026-03-15*
*System: autonomy-platform (Go 1.23 + Python 3.11)*
*Current state: Phase 9 complete — mock exchange operational*
*Next action: Phase 1 backtest validation*
