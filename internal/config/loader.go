package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Policy struct {
	Mode      string    `yaml:"mode"`
	Execution Execution `yaml:"execution"`

	AllowedMarkets map[string][]string `yaml:"allowed_markets"`

	PerTrade    PerTradeLimits    `yaml:"per_trade"`
	PerPosition PerPositionLimits `yaml:"per_position"`
	PerStrategy PerStrategyLimits `yaml:"per_strategy"`
	PerVenue    map[string]VenueLimits `yaml:"per_venue"`
	Global      GlobalLimits      `yaml:"global"`
	DataQuality DataQuality       `yaml:"data_quality"`
	KillSwitch  KillSwitchTriggers `yaml:"kill_switch_triggers"`
}

type Execution struct {
	TransmitOrders        bool `yaml:"transmit_orders"`
	RequireSignedApproval bool `yaml:"require_signed_approval"`
	RequireHeartbeat      bool `yaml:"require_heartbeat"`
}

type PerTradeLimits struct {
	MaxNotionalMicros int64 `yaml:"max_notional_micros"`
	MaxQuantity       int32 `yaml:"max_quantity"`
	MinPriceMicros    int64 `yaml:"min_price_micros"`
	MaxPriceMicros    int64 `yaml:"max_price_micros"`
	MaxSpreadBps      int64 `yaml:"max_spread_bps"`
}

type PerPositionLimits struct {
	MaxNotionalMicros    int64   `yaml:"max_notional_micros"`
	MaxQuantity          int32   `yaml:"max_quantity"`
	MaxConcentrationPct  float64 `yaml:"max_concentration_pct"`
}

type PerStrategyLimits struct {
	MaxDailyLossMicros     int64 `yaml:"max_daily_loss_micros"`
	MaxDailyTurnoverMicros int64 `yaml:"max_daily_turnover_micros"`
	MaxOrdersPerMinute     int32 `yaml:"max_orders_per_minute"`
	MaxConsecutiveLosses   int32 `yaml:"max_consecutive_losses"`
	MaxOpenOrders          int32 `yaml:"max_open_orders"`
}

type VenueLimits struct {
	MaxExposureMicros  int64 `yaml:"max_exposure_micros"`
	MaxDailyLossMicros int64 `yaml:"max_daily_loss_micros"`
}

type GlobalLimits struct {
	MaxTotalExposureMicros int64   `yaml:"max_total_exposure_micros"`
	MaxDailyLossMicros     int64   `yaml:"max_daily_loss_micros"`
	MaxDrawdownPct         float64 `yaml:"max_drawdown_from_peak_pct"`
	TradingHoursStart      string  `yaml:"trading_hours_utc_start"`
	TradingHoursEnd        string  `yaml:"trading_hours_utc_end"`
}

type DataQuality struct {
	MaxDataAgeSeconds int32 `yaml:"max_data_age_seconds"`
	MinOrderbookDepth int32 `yaml:"min_orderbook_depth"`
}

type KillSwitchTriggers struct {
	GlobalDailyLossMicros   int64   `yaml:"global_daily_loss_micros"`
	GlobalDrawdownPct       float64 `yaml:"global_drawdown_pct"`
	VenueDailyLossMicros    int64   `yaml:"venue_daily_loss_micros"`
	StrategyDailyLossMicros int64   `yaml:"strategy_daily_loss_micros"`
	StrategyConsecLosses    int32   `yaml:"strategy_consecutive_losses"`
	HeartbeatTimeoutSec     int32   `yaml:"heartbeat_timeout_seconds"`
}

func LoadPolicy(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy file: %w", err)
	}
	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse policy file: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid policy: %w", err)
	}
	return &p, nil
}

func (p *Policy) Validate() error {
	if p.Mode == "" {
		return fmt.Errorf("mode is required")
	}
	if p.PerTrade.MaxNotionalMicros <= 0 {
		return fmt.Errorf("per_trade.max_notional_micros must be positive")
	}
	if p.Global.MaxDailyLossMicros <= 0 {
		return fmt.Errorf("global.max_daily_loss_micros must be positive")
	}
	return nil
}

// ConfigHash returns a deterministic hash of the policy for audit trails.
func (p *Policy) ConfigHash() string {
	data, _ := yaml.Marshal(p)
	// Use a simple hash — this is for audit identification, not security.
	var h uint64
	for _, b := range data {
		h = h*31 + uint64(b)
	}
	return fmt.Sprintf("policy:%016x", h)
}
