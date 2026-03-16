package backtest

import (
	"encoding/json"
	"fmt"
	"io"
)

// PrintTable prints backtest results as a formatted ASCII table.
func PrintTable(w io.Writer, r *RunResult) {
	fmt.Fprintf(w, "=== Backtest Results ===\n")
	fmt.Fprintf(w, "Strategy:        %s\n", r.Config.StrategyName)
	fmt.Fprintf(w, "Venue:           %s\n", r.Config.Venue)
	fmt.Fprintf(w, "Fill mode:       %s\n", r.Config.FillMode)
	fmt.Fprintf(w, "Initial capital: $%.2f\n", float64(r.Config.InitialCapital)/1e6)
	fmt.Fprintf(w, "Ticks processed: %d\n", len(r.Config.Ticks))
	fmt.Fprintf(w, "Duration:        %s\n", r.Duration)
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "--- Performance ---\n")
	fmt.Fprintf(w, "Total Return:    %.4f%%\n", r.Metrics.TotalReturn*100)
	fmt.Fprintf(w, "Total P&L:       $%.2f\n", float64(r.Metrics.TotalPnL)/1e6)
	fmt.Fprintf(w, "Sharpe Ratio:    %.4f\n", r.Metrics.SharpeRatio)
	fmt.Fprintf(w, "Max Drawdown:    %.4f%%\n", r.Metrics.MaxDrawdown*100)
	fmt.Fprintf(w, "Win Rate:        %.2f%%\n", r.Metrics.WinRate*100)
	if r.Metrics.ProfitFactor > 0 {
		fmt.Fprintf(w, "Profit Factor:   %.4f\n", r.Metrics.ProfitFactor)
	} else {
		fmt.Fprintf(w, "Profit Factor:   N/A\n")
	}
	fmt.Fprintf(w, "Trade Count:     %d\n", r.Metrics.TradeCount)

	if len(r.Trades) > 0 {
		fmt.Fprintf(w, "\n--- Recent Trades (last 20) ---\n")
		start := 0
		if len(r.Trades) > 20 {
			start = len(r.Trades) - 20
		}
		for i := start; i < len(r.Trades); i++ {
			t := r.Trades[i]
			fmt.Fprintf(w, "  %s %s %d @ $%.2f  P&L: $%.2f\n",
				t.Side, t.MarketID, t.Quantity,
				float64(t.PriceMicros)/1e6, float64(t.PnLMicros)/1e6)
		}
	}
}

// PrintJSON outputs results as JSON.
func PrintJSON(w io.Writer, r *RunResult) error {
	out := struct {
		Strategy       string          `json:"strategy"`
		Venue          string          `json:"venue"`
		FillMode       string          `json:"fill_mode"`
		InitialCapital float64         `json:"initial_capital"`
		Metrics        BacktestMetrics `json:"metrics"`
		Trades         []Trade         `json:"trades"`
	}{
		Strategy:       r.Config.StrategyName,
		Venue:          r.Config.Venue,
		FillMode:       r.Config.FillMode,
		InitialCapital: float64(r.Config.InitialCapital) / 1e6,
		Metrics:        r.Metrics,
		Trades:         r.Trades,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
