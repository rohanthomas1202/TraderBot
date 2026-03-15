package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"autonomy-platform/internal/config"
	"autonomy-platform/services/risk"
	"autonomy-platform/services/watchdog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// trade-ctl is the Phase 1 operator CLI tool.
// It provides break-glass and operational control without requiring a web dashboard.
//
// Usage:
//   trade-ctl status                         — show system status
//   trade-ctl kill --level cancel_only       — trigger global kill switch
//   trade-ctl kill --level soft_pause --scope strategy:momentum-v1
//   trade-ctl ack --scope global --cause "investigated, false alarm"
//   trade-ctl resume --scope global
//   trade-ctl orders                         — list open orders
//   trade-ctl risk                           — show risk state
//   trade-ctl limits                         — show active limits
//   trade-ctl policy                         — show loaded policy summary

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	ctx := context.Background()
	cmd := os.Args[1]

	switch cmd {
	case "status":
		cmdStatus(ctx)
	case "kill":
		cmdKill(ctx)
	case "ack":
		cmdAck(ctx)
	case "resume":
		cmdResume(ctx)
	case "orders":
		cmdOrders(ctx)
	case "risk":
		cmdRisk(ctx)
	case "limits":
		cmdLimits(ctx)
	case "policy":
		cmdPolicy(ctx)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func cmdStatus(ctx context.Context) {
	db := mustConnectDB(ctx)
	defer db.Close()

	// Query active halts
	rows, err := db.Query(ctx,
		`SELECT level, scope, reason, triggered_by, triggered_at, acknowledged
		 FROM watchdog.kill_switch_events WHERE resumed = FALSE
		 ORDER BY triggered_at DESC`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query failed: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Println("=== System Status ===")
	hasHalts := false
	for rows.Next() {
		var h watchdog.ActiveHalt
		if err := rows.Scan(&h.Level, &h.Scope, &h.Reason, &h.TriggeredBy, &h.TriggeredAt, &h.Acknowledged); err != nil {
			fmt.Fprintf(os.Stderr, "scan: %v\n", err)
			continue
		}
		hasHalts = true
		ackStatus := "NOT ACKNOWLEDGED"
		if h.Acknowledged {
			ackStatus = "acknowledged"
		}
		fmt.Printf("  HALT: level=%s scope=%s reason=%q by=%s at=%s [%s]\n",
			h.Level, h.Scope, h.Reason, h.TriggeredBy,
			h.TriggeredAt.Format("15:04:05"), ackStatus)
	}
	if !hasHalts {
		fmt.Println("  Mode: NORMAL (no active halts)")
	}

	// Query open order count
	var openCount int
	db.QueryRow(ctx, `SELECT COUNT(*) FROM execution.orders WHERE status IN ('pending','open','partially_filled')`).Scan(&openCount)
	fmt.Printf("  Open orders: %d\n", openCount)

	// Query today's stats
	var pnl int64
	db.QueryRow(ctx, `SELECT COALESCE(pnl_micros, 0) FROM risk.daily_stats WHERE date = CURRENT_DATE AND scope = 'global'`).Scan(&pnl)
	fmt.Printf("  Daily P&L: $%.2f\n", float64(pnl)/1_000_000)
}

func cmdKill(ctx context.Context) {
	level := flagValue("--level", "cancel_only")
	scope := flagValue("--scope", "global")
	reason := flagValue("--reason", "manual operator kill via trade-ctl")

	db := mustConnectDB(ctx)
	defer db.Close()

	_, err := db.Exec(ctx,
		`INSERT INTO watchdog.kill_switch_events (level, scope, reason, triggered_by)
		 VALUES ($1, $2, $3, $4)`,
		level, scope, reason, "trade-ctl:"+os.Getenv("USER"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to insert kill switch event: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("KILL SWITCH ACTIVATED: level=%s scope=%s\n", level, scope)
	fmt.Println("Services will pick up the halt on next health check.")
	fmt.Println("To acknowledge: trade-ctl ack --scope", scope, "--cause \"<reason>\"")
}

func cmdAck(ctx context.Context) {
	scope := flagValue("--scope", "global")
	cause := flagValue("--cause", "")
	if cause == "" {
		fmt.Fprintf(os.Stderr, "error: --cause is required\n")
		os.Exit(1)
	}

	db := mustConnectDB(ctx)
	defer db.Close()

	result, err := db.Exec(ctx,
		`UPDATE watchdog.kill_switch_events
		 SET acknowledged = TRUE, acknowledged_by = $1, acknowledged_at = NOW(), root_cause = $2
		 WHERE scope = $3 AND resumed = FALSE AND acknowledged = FALSE`,
		"trade-ctl:"+os.Getenv("USER"), cause, scope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed: %v\n", err)
		os.Exit(1)
	}
	if result.RowsAffected() == 0 {
		fmt.Println("No unacknowledged halt found for scope:", scope)
	} else {
		fmt.Printf("Halt on %s acknowledged. To resume: trade-ctl resume --scope %s\n", scope, scope)
	}
}

func cmdResume(ctx context.Context) {
	scope := flagValue("--scope", "global")

	db := mustConnectDB(ctx)
	defer db.Close()

	// Check that halt is acknowledged first
	var acked bool
	err := db.QueryRow(ctx,
		`SELECT acknowledged FROM watchdog.kill_switch_events
		 WHERE scope = $1 AND resumed = FALSE LIMIT 1`, scope).Scan(&acked)
	if err != nil {
		fmt.Println("No active halt found for scope:", scope)
		return
	}
	if !acked {
		fmt.Fprintf(os.Stderr, "error: halt must be acknowledged before resuming. Use: trade-ctl ack --scope %s --cause \"...\"\n", scope)
		os.Exit(1)
	}

	_, err = db.Exec(ctx,
		`UPDATE watchdog.kill_switch_events
		 SET resumed = TRUE, resumed_by = $1, resumed_at = NOW()
		 WHERE scope = $2 AND resumed = FALSE`,
		"trade-ctl:"+os.Getenv("USER"), scope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Trading resumed on scope %s\n", scope)
}

func cmdOrders(ctx context.Context) {
	db := mustConnectDB(ctx)
	defer db.Close()

	rows, err := db.Query(ctx,
		`SELECT internal_order_id, trace_id, strategy_id, market_id, side, quantity,
		        price_micros, status, filled_quantity, created_at
		 FROM execution.orders
		 WHERE status IN ('pending','open','partially_filled')
		 ORDER BY created_at DESC LIMIT 20`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query failed: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Println("=== Open Orders ===")
	for rows.Next() {
		var id, traceID, stratID, mktID, side, status string
		var qty, filled int32
		var price int64
		var created interface{}
		rows.Scan(&id, &traceID, &stratID, &mktID, &side, &qty, &price, &status, &filled, &created)
		fmt.Printf("  %s | %s | %s %s %d@$%.2f | filled=%d | %s\n",
			id[:8], stratID, side, mktID, qty, float64(price)/1_000_000, filled, status)
	}
}

func cmdRisk(ctx context.Context) {
	db := mustConnectDB(ctx)
	defer db.Close()

	fmt.Println("=== Risk State ===")
	rows, err := db.Query(ctx,
		`SELECT scope, pnl_micros, turnover_micros, order_count, fill_count
		 FROM risk.daily_stats WHERE date = CURRENT_DATE ORDER BY scope`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query failed: %v\n", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var scope string
		var pnl, turnover int64
		var orders, fills int32
		rows.Scan(&scope, &pnl, &turnover, &orders, &fills)
		fmt.Printf("  %s: P&L=$%.2f turnover=$%.2f orders=%d fills=%d\n",
			scope, float64(pnl)/1e6, float64(turnover)/1e6, orders, fills)
	}
}

func cmdLimits(ctx context.Context) {
	db := mustConnectDB(ctx)
	defer db.Close()

	rows, _ := db.Query(ctx,
		`SELECT limit_name, scope, value_micros, unit FROM risk.limits WHERE active = TRUE ORDER BY scope, limit_name`)
	defer rows.Close()

	fmt.Println("=== Active Limits ===")
	for rows.Next() {
		var name, scope, unit string
		var value int64
		rows.Scan(&name, &scope, &value, &unit)
		fmt.Printf("  %-35s [%-20s] = %d %s\n", name, scope, value, unit)
	}
}

func cmdPolicy(ctx context.Context) {
	policyPath := envOrDefault("POLICY_FILE", "./policies/paper.yaml")
	policy, err := config.LoadPolicy(policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load policy: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("=== Policy: %s ===\n", policyPath)
	fmt.Printf("  Mode: %s\n", policy.Mode)
	fmt.Printf("  Hash: %s\n", policy.ConfigHash())
	fmt.Printf("  Max trade notional: $%.2f\n", float64(policy.PerTrade.MaxNotionalMicros)/1e6)
	fmt.Printf("  Max daily loss: $%.2f\n", float64(policy.Global.MaxDailyLossMicros)/1e6)
	fmt.Printf("  Max drawdown: %.1f%%\n", policy.Global.MaxDrawdownPct)

	// Suppress unused import errors
	_ = risk.DecisionApproved
}

func mustConnectDB(ctx context.Context) *pgxpool.Pool {
	db, err := pgxpool.New(ctx, envOrDefault("POSTGRES_URL", "postgres://trader:localdev@localhost:5432/autonomy?sslmode=disable"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to database: %v\n", err)
		os.Exit(1)
	}
	return db
}

func flagValue(flag, defaultVal string) string {
	for i, arg := range os.Args {
		if arg == flag && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
		if strings.HasPrefix(arg, flag+"=") {
			return strings.TrimPrefix(arg, flag+"=")
		}
	}
	return defaultVal
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: trade-ctl <command> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  status   Show system status")
	fmt.Fprintln(os.Stderr, "  kill     Trigger kill switch (--level, --scope, --reason)")
	fmt.Fprintln(os.Stderr, "  ack      Acknowledge a halt (--scope, --cause)")
	fmt.Fprintln(os.Stderr, "  resume   Resume trading (--scope)")
	fmt.Fprintln(os.Stderr, "  orders   List open orders")
	fmt.Fprintln(os.Stderr, "  risk     Show risk state")
	fmt.Fprintln(os.Stderr, "  limits   Show active limits")
	fmt.Fprintln(os.Stderr, "  policy   Show loaded policy summary")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
