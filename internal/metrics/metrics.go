package metrics

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var registerOnce sync.Once

// --- Counters ---

var OrdersSubmittedTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "orders_submitted_total",
		Help: "Total orders submitted to venue",
	},
	[]string{"venue", "strategy_id", "status"},
)

var OrdersFilledTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "orders_filled_total",
		Help: "Total orders fully filled",
	},
	[]string{"venue", "strategy_id"},
)

var OrdersRejectedTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "orders_rejected_total",
		Help: "Total orders rejected by risk engine",
	},
	[]string{"venue", "strategy_id", "reason"},
)

var RiskChecksTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "risk_checks_total",
		Help: "Total risk check evaluations",
	},
	[]string{"check_name", "result"},
)

var KillsTriggeredTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "kills_triggered_total",
		Help: "Total kill switch activations",
	},
	[]string{"level", "scope"},
)

var FillsProcessedTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "fills_processed_total",
		Help: "Total fills processed",
	},
	[]string{"venue"},
)

// --- Histograms ---

var OrderToFillLatency = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "order_to_fill_latency_seconds",
		Help:    "Latency from order submission to fill",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	},
	[]string{"venue"},
)

var RiskCheckDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "risk_check_duration_seconds",
		Help:    "Duration of individual risk check evaluation",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05},
	},
	[]string{"check_name"},
)

var DataIngestionLatency = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "data_ingestion_latency_seconds",
		Help:    "Latency of market data ingestion cycle",
		Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5},
	},
	[]string{"venue"},
)

var GRPCRequestDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "grpc_request_duration_seconds",
		Help:    "Duration of gRPC server requests",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1},
	},
	[]string{"service", "method", "code"},
)

// --- Gauges ---

var TotalExposureMicros = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "total_exposure_micros",
		Help: "Current total exposure in microdollars",
	},
)

var DailyPnlMicros = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "daily_pnl_micros",
		Help: "Current daily P&L in microdollars",
	},
)

var OpenOrdersCount = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "open_orders_count",
		Help: "Number of currently open orders",
	},
	[]string{"venue"},
)

var MarketDataAgeSec = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "market_data_age_seconds",
		Help: "Age of latest market data in seconds",
	},
	[]string{"venue", "market_id"},
)

var StrategyConsecutiveLosses = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "strategy_consecutive_losses",
		Help: "Current consecutive losses per strategy",
	},
	[]string{"strategy_id"},
)

var PeakEquityMicros = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "peak_equity_micros",
		Help: "Peak equity watermark in microdollars",
	},
)

var CurrentEquityMicros = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "current_equity_micros",
		Help: "Current equity in microdollars",
	},
)

// MustRegister registers all metrics with the default Prometheus registry.
// Safe to call multiple times.
func MustRegister() {
	registerOnce.Do(func() {
		// Counters
		prometheus.MustRegister(OrdersSubmittedTotal)
		prometheus.MustRegister(OrdersFilledTotal)
		prometheus.MustRegister(OrdersRejectedTotal)
		prometheus.MustRegister(RiskChecksTotal)
		prometheus.MustRegister(KillsTriggeredTotal)
		prometheus.MustRegister(FillsProcessedTotal)

		// Histograms
		prometheus.MustRegister(OrderToFillLatency)
		prometheus.MustRegister(RiskCheckDuration)
		prometheus.MustRegister(DataIngestionLatency)
		prometheus.MustRegister(GRPCRequestDuration)

		// Gauges
		prometheus.MustRegister(TotalExposureMicros)
		prometheus.MustRegister(DailyPnlMicros)
		prometheus.MustRegister(OpenOrdersCount)
		prometheus.MustRegister(MarketDataAgeSec)
		prometheus.MustRegister(StrategyConsecutiveLosses)
		prometheus.MustRegister(PeakEquityMicros)
		prometheus.MustRegister(CurrentEquityMicros)

		// Go runtime metrics
		prometheus.MustRegister(collectors.NewBuildInfoCollector())
	})
}

// Handler returns an HTTP handler for the /metrics endpoint.
func Handler() http.Handler {
	return promhttp.Handler()
}
