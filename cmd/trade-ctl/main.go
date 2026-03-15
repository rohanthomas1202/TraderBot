package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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
	case "audit":
		cmdAudit(ctx)
	case "trace":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: trade-ctl trace <trace_id>")
			os.Exit(1)
		}
		cmdTrace(ctx, os.Args[2])
	case "config":
		cmdConfig(ctx)
	case "ledger":
		cmdLedger(ctx)
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

	// Probe health endpoints
	fmt.Println("\n=== Service Health ===")
	services := map[string]string{
		"execution-engine": envOrDefault("EXEC_HEALTH_URL", "http://localhost:50041/health"),
		"risk-engine":      envOrDefault("RISK_HEALTH_URL", "http://localhost:50021/health"),
		"watchdog":         envOrDefault("WATCHDOG_HEALTH_URL", "http://localhost:50056/health"),
		"strategy-engine":  envOrDefault("STRATEGY_HEALTH_URL", "http://localhost:50031/health"),
		"data-ingestion":   envOrDefault("DATA_HEALTH_URL", "http://localhost:50011/health"),
	}
	httpClient := &http.Client{Timeout: 2 * time.Second}
	for name, url := range services {
		resp, err := httpClient.Get(url)
		if err != nil {
			fmt.Printf("  %-20s UNREACHABLE\n", name)
			continue
		}
		var health struct {
			Status string `json:"status"`
			Uptime string `json:"uptime"`
		}
		json.NewDecoder(resp.Body).Decode(&health)
		resp.Body.Close()
		fmt.Printf("  %-20s %s (uptime: %s)\n", name, strings.ToUpper(health.Status), health.Uptime)
	}
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

func cmdAudit(ctx context.Context) {
	db := mustConnectDB(ctx)
	defer db.Close()

	eventType := flagValue("--type", "")
	severity := flagValue("--severity", "")
	limitStr := flagValue("--limit", "50")
	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 {
		limit = 50
	}

	rows, err := db.Query(ctx,
		`SELECT id, timestamp, service, event_type, trace_id, severity,
		        LEFT(payload::text, 120) AS payload_preview
		 FROM audit.event_log
		 WHERE ($1 = '' OR event_type = $1)
		   AND ($2 = '' OR severity = $2)
		 ORDER BY timestamp DESC
		 LIMIT $3`,
		eventType, severity, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query failed: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Println("=== Audit Log ===")
	count := 0
	for rows.Next() {
		var id, service, evType, sev string
		var traceID *string
		var payload *string
		var ts time.Time
		rows.Scan(&id, &ts, &service, &evType, &traceID, &sev, &payload)

		tid := "-"
		if traceID != nil && *traceID != "" {
			tid = (*traceID)[:8]
		}
		prev := ""
		if payload != nil {
			prev = *payload
		}
		fmt.Printf("  %s | %-14s | %-20s | [%-8s] | trace=%s | %s\n",
			ts.Format("15:04:05"), service, evType, sev, tid, prev)
		count++
	}
	if count == 0 {
		fmt.Println("  (no audit entries found)")
	}
}

func cmdTrace(ctx context.Context, traceID string) {
	db := mustConnectDB(ctx)
	defer db.Close()

	fmt.Printf("=== Trace: %s ===\n", traceID)

	// 1. Intent ledger entry (trace_id is TEXT)
	fmt.Println("\n--- Intent Ledger ---")
	var intentID int64
	var version int64
	var strategyID, marketID, side, intentStatus string
	var qty int32
	var price, notional int64
	var intentCreated, intentUpdated time.Time
	err := db.QueryRow(ctx,
		`SELECT intent_id, version, strategy_id, market_id, side,
		        quantity, price_micros, notional_micros, status, created_at, updated_at
		 FROM execution.order_intents
		 WHERE trace_id = $1`, traceID).Scan(
		&intentID, &version, &strategyID, &marketID, &side,
		&qty, &price, &notional, &intentStatus, &intentCreated, &intentUpdated)
	if err != nil {
		fmt.Println("  (no intent ledger entry found)")
	} else {
		fmt.Printf("  version=%d | %s | %s | %s %d@$%.6f ($%.2f) | status=%s\n",
			version, strategyID, marketID, side, qty,
			float64(price)/1e6, float64(notional)/1e6, intentStatus)
		fmt.Printf("  created=%s  updated=%s\n",
			intentCreated.Format("15:04:05"), intentUpdated.Format("15:04:05"))
	}

	// 2. Risk decision (trace_id is UUID)
	fmt.Println("\n--- Risk Decision ---")
	var decision, policyHash string
	var checksJSON *string
	var decidedAt time.Time
	err = db.QueryRow(ctx,
		`SELECT decision, checks_json, policy_config_hash, decided_at
		 FROM risk.policy_decisions
		 WHERE trace_id = $1::uuid
		 ORDER BY decided_at DESC LIMIT 1`, traceID).Scan(
		&decision, &checksJSON, &policyHash, &decidedAt)
	if err != nil {
		fmt.Println("  (no risk decision found)")
	} else {
		fmt.Printf("  decision=%s  policy=%s  decided=%s\n",
			decision, policyHash, decidedAt.Format("15:04:05"))
		if checksJSON != nil {
			fmt.Printf("  checks: %s\n", *checksJSON)
		}
	}

	// 3. Order record (trace_id is UUID)
	fmt.Println("\n--- Order ---")
	var internalOrderID, exchangeOrderID, orderStatus string
	var filledQty int32
	var avgFillPrice int64
	var submittedAt, completedAt *time.Time
	var orderQuantity int32
	err = db.QueryRow(ctx,
		`SELECT internal_order_id, exchange_order_id, status, quantity, filled_quantity,
		        avg_fill_price_micros, submitted_at, completed_at
		 FROM execution.orders
		 WHERE trace_id = $1::uuid`, traceID).Scan(
		&internalOrderID, &exchangeOrderID, &orderStatus, &orderQuantity, &filledQty,
		&avgFillPrice, &submittedAt, &completedAt)
	if err != nil {
		fmt.Println("  (no order record found)")
	} else {
		subStr := "-"
		if submittedAt != nil {
			subStr = submittedAt.Format("15:04:05")
		}
		compStr := "-"
		if completedAt != nil {
			compStr = completedAt.Format("15:04:05")
		}
		fmt.Printf("  id=%s  exchange=%s  status=%s  filled=%d/%d  avg=$%.6f\n",
			internalOrderID[:8], exchangeOrderID, orderStatus, filledQty, orderQuantity,
			float64(avgFillPrice)/1e6)
		fmt.Printf("  submitted=%s  completed=%s\n", subStr, compStr)

		// 4. Fills (by internal_order_id)
		fillRows, err := db.Query(ctx,
			`SELECT fill_id, quantity, price_micros, fee_micros, filled_at
			 FROM execution.fills
			 WHERE internal_order_id = $1::uuid
			 ORDER BY filled_at`, internalOrderID)
		if err == nil {
			defer fillRows.Close()
			fmt.Println("\n--- Fills ---")
			i := 0
			for fillRows.Next() {
				var fillID string
				var fQty int32
				var fPrice, fFee int64
				var fAt time.Time
				fillRows.Scan(&fillID, &fQty, &fPrice, &fFee, &fAt)
				i++
				fmt.Printf("  [%d] %d@$%.6f fee=$%.2f at=%s\n",
					i, fQty, float64(fPrice)/1e6, float64(fFee)/1e6, fAt.Format("15:04:05"))
			}
			if i == 0 {
				fmt.Println("  (no fills)")
			}
		}
	}

	// 5. Related audit entries
	fmt.Println("\n--- Audit Trail ---")
	auditRows, err := db.Query(ctx,
		`SELECT timestamp, service, event_type, severity, LEFT(payload::text, 100)
		 FROM audit.event_log
		 WHERE trace_id = $1::uuid
		 ORDER BY timestamp`, traceID)
	if err != nil {
		fmt.Println("  (no audit entries)")
		return
	}
	defer auditRows.Close()
	auditCount := 0
	for auditRows.Next() {
		var ts time.Time
		var svc, evType, sev string
		var payload *string
		auditRows.Scan(&ts, &svc, &evType, &sev, &payload)
		prev := ""
		if payload != nil {
			prev = *payload
		}
		fmt.Printf("  %s %-14s %-20s [%-8s] %s\n",
			ts.Format("15:04:05"), svc, evType, sev, prev)
		auditCount++
	}
	if auditCount == 0 {
		fmt.Println("  (no audit entries)")
	}
}

func cmdConfig(ctx context.Context) {
	policyPath := envOrDefault("POLICY_FILE", "./policies/paper.yaml")

	// Load and display policy
	policy, err := config.LoadPolicy(policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load policy: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== Running Configuration ===")
	fmt.Printf("Policy file: %s\n", policyPath)
	fmt.Printf("Policy hash: %s\n", policy.ConfigHash())
	fmt.Printf("Mode: %s\n\n", policy.Mode)

	// Display raw policy YAML
	fmt.Println("--- Policy YAML ---")
	yamlData, err := os.ReadFile(policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  (could not read policy file: %v)\n", err)
	} else {
		fmt.Println(string(yamlData))
	}

	// Display active runtime limits
	db := mustConnectDB(ctx)
	defer db.Close()

	fmt.Println("--- Active Runtime Limits ---")
	rows, err := db.Query(ctx,
		`SELECT limit_name, scope, value_micros, unit, updated_by, updated_at
		 FROM risk.limits WHERE active = TRUE
		 ORDER BY scope, limit_name`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  (query failed: %v)\n", err)
	} else {
		defer rows.Close()
		count := 0
		for rows.Next() {
			var name, scope, unit, updatedBy string
			var value int64
			var updatedAt time.Time
			rows.Scan(&name, &scope, &value, &unit, &updatedBy, &updatedAt)
			fmt.Printf("  %-35s [%-20s] = %d %s  (by: %s, at: %s)\n",
				name, scope, value, unit, updatedBy, updatedAt.Format("2006-01-02"))
			count++
		}
		if count == 0 {
			fmt.Println("  (no active limits)")
		}
	}

	// Display recent config changes
	fmt.Println("\n--- Recent Config Changes ---")
	changeRows, err := db.Query(ctx,
		`SELECT config_type, scope, changed_by, reason, changed_at
		 FROM audit.config_changes
		 ORDER BY changed_at DESC LIMIT 10`)
	if err != nil {
		fmt.Println("  (none)")
	} else {
		defer changeRows.Close()
		count := 0
		for changeRows.Next() {
			var configType, scope, changedBy, reason string
			var changedAt time.Time
			changeRows.Scan(&configType, &scope, &changedBy, &reason, &changedAt)
			fmt.Printf("  %s | %s | %s | by=%s | %s\n",
				changedAt.Format("2006-01-02 15:04"), configType, scope, changedBy, reason)
			count++
		}
		if count == 0 {
			fmt.Println("  (none)")
		}
	}
}

func cmdLedger(ctx context.Context) {
	db := mustConnectDB(ctx)
	defer db.Close()

	statusFilter := flagValue("--status", "")
	marketFilter := flagValue("--market", "")
	limitStr := flagValue("--limit", "20")
	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 {
		limit = 20
	}

	// Recent intents
	rows, err := db.Query(ctx,
		`SELECT intent_id, version, trace_id, strategy_id, market_id, side,
		        quantity, price_micros, notional_micros, status, created_at
		 FROM execution.order_intents
		 WHERE ($1 = '' OR status = $1)
		   AND ($2 = '' OR market_id = $2)
		 ORDER BY version DESC
		 LIMIT $3`,
		statusFilter, marketFilter, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query failed: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Println("=== Intent Ledger ===")
	count := 0
	for rows.Next() {
		var intentID, version int64
		var traceID, stratID, mktID, side, st string
		var qty int32
		var price, notional int64
		var created time.Time
		rows.Scan(&intentID, &version, &traceID, &stratID, &mktID, &side,
			&qty, &price, &notional, &st, &created)

		tid := traceID
		if len(tid) > 8 {
			tid = tid[:8]
		}
		fmt.Printf("  v%-4d | %s | %-18s | %-15s | %s %d@$%.6f ($%.2f) | %-8s | %s\n",
			version, tid, stratID, mktID, side, qty,
			float64(price)/1e6, float64(notional)/1e6, st,
			created.Format("15:04:05"))
		count++
	}
	if count == 0 {
		fmt.Println("  (no intents found)")
	}

	// Exposure by market
	fmt.Println("\n=== Exposure by Market ===")
	expRows, err := db.Query(ctx,
		`SELECT market_id, side, SUM(quantity) AS qty, SUM(notional_micros) AS notional,
		        COUNT(*) AS intent_count
		 FROM execution.order_intents
		 WHERE status IN ('pending', 'open')
		 GROUP BY market_id, side
		 ORDER BY market_id, side`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  (query failed: %v)\n", err)
		return
	}
	defer expRows.Close()

	expCount := 0
	for expRows.Next() {
		var mktID, side string
		var qty int64
		var notional int64
		var intentCount int32
		expRows.Scan(&mktID, &side, &qty, &notional, &intentCount)
		fmt.Printf("  %-20s %s: qty=%d  notional=$%.2f  (%d intents)\n",
			mktID, side, qty, float64(notional)/1e6, intentCount)
		expCount++
	}
	if expCount == 0 {
		fmt.Println("  (no outstanding exposure)")
	}

	// Total exposure
	var totalNotional int64
	db.QueryRow(ctx,
		`SELECT COALESCE(SUM(notional_micros), 0)
		 FROM execution.order_intents
		 WHERE status IN ('pending', 'open')`).Scan(&totalNotional)
	fmt.Printf("\nTotal outstanding exposure: $%.2f\n", float64(totalNotional)/1e6)
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
	fmt.Fprintln(os.Stderr, "  audit    Query audit log (--type, --severity, --limit)")
	fmt.Fprintln(os.Stderr, "  trace    Full lifecycle trace (trace <trace_id>)")
	fmt.Fprintln(os.Stderr, "  config   Show running configuration")
	fmt.Fprintln(os.Stderr, "  ledger   Query intent ledger (--status, --market, --limit)")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
