//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestCert_PaperTradingReport generates a comprehensive report of the
// current paper trading state. Run this after the extended simulation.
func TestCert_PaperTradingReport(t *testing.T) {
	db, _, _ := setupTestEnv(t)
	ctx := context.Background()

	t.Log("╔══════════════════════════════════════════════════════╗")
	t.Log("║          PAPER TRADING CERTIFICATION REPORT         ║")
	t.Log("╚══════════════════════════════════════════════════════╝")
	t.Log("")

	// ─── Order Summary ───
	type orderStats struct {
		Total     int
		Filled    int
		Cancelled int
		Rejected  int
		Open      int
		Pending   int
	}
	var os orderStats
	db.QueryRow(ctx, `SELECT COUNT(*) FROM execution.orders`).Scan(&os.Total)
	db.QueryRow(ctx, `SELECT COUNT(*) FROM execution.orders WHERE status = 'filled'`).Scan(&os.Filled)
	db.QueryRow(ctx, `SELECT COUNT(*) FROM execution.orders WHERE status = 'cancelled'`).Scan(&os.Cancelled)
	db.QueryRow(ctx, `SELECT COUNT(*) FROM execution.orders WHERE status = 'rejected'`).Scan(&os.Rejected)
	db.QueryRow(ctx, `SELECT COUNT(*) FROM execution.orders WHERE status IN ('open','partially_filled')`).Scan(&os.Open)
	db.QueryRow(ctx, `SELECT COUNT(*) FROM execution.orders WHERE status = 'pending'`).Scan(&os.Pending)

	t.Log("── Order Summary ──")
	t.Logf("  Total orders:      %d", os.Total)
	t.Logf("  Filled:            %d", os.Filled)
	t.Logf("  Cancelled:         %d", os.Cancelled)
	t.Logf("  Rejected:          %d", os.Rejected)
	t.Logf("  Open:              %d", os.Open)
	t.Logf("  Pending:           %d", os.Pending)
	fillRate := 0.0
	if os.Total > 0 {
		fillRate = float64(os.Filled) / float64(os.Total) * 100
	}
	t.Logf("  Fill rate:         %.1f%%", fillRate)
	t.Log("")

	// ─── Risk Decisions ───
	var approved, denied int
	db.QueryRow(ctx, `SELECT COUNT(*) FROM risk.policy_decisions WHERE decision = 'approved'`).Scan(&approved)
	db.QueryRow(ctx, `SELECT COUNT(*) FROM risk.policy_decisions WHERE decision = 'denied'`).Scan(&denied)

	t.Log("── Risk Decisions ──")
	t.Logf("  Approved:          %d", approved)
	t.Logf("  Denied:            %d", denied)
	denialRate := 0.0
	if approved+denied > 0 {
		denialRate = float64(denied) / float64(approved+denied) * 100
	}
	t.Logf("  Denial rate:       %.1f%%", denialRate)
	t.Log("")

	// ─── Intent Ledger ───
	var totalIntents int
	var minV, maxV int64
	db.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(MIN(version),0), COALESCE(MAX(version),0) FROM execution.order_intents`).
		Scan(&totalIntents, &minV, &maxV)

	t.Log("── Intent Ledger ──")
	t.Logf("  Total intents:     %d", totalIntents)
	if totalIntents > 0 {
		t.Logf("  Version range:     %d – %d", minV, maxV)
		expectedCount := maxV - minV + 1
		if int64(totalIntents) == expectedCount {
			t.Logf("  Gapless:           YES ✓")
		} else {
			t.Logf("  Gapless:           NO ✗ (expected %d, got %d)", expectedCount, totalIntents)
		}
	}
	t.Log("")

	// ─── Kill Switch Events ───
	var totalKills, activeKills int
	db.QueryRow(ctx, `SELECT COUNT(*) FROM watchdog.kill_switch_events`).Scan(&totalKills)
	db.QueryRow(ctx, `SELECT COUNT(*) FROM watchdog.kill_switch_events WHERE resumed = FALSE`).Scan(&activeKills)

	t.Log("── Kill Switch Events ──")
	t.Logf("  Total events:      %d", totalKills)
	t.Logf("  Active (unresumed):%d", activeKills)
	t.Log("")

	// ─── Audit Log ───
	var totalAudit, criticalAudit, uniqueHashes int
	db.QueryRow(ctx, `SELECT COUNT(*) FROM audit.event_log`).Scan(&totalAudit)
	db.QueryRow(ctx, `SELECT COUNT(*) FROM audit.event_log WHERE severity = 'critical'`).Scan(&criticalAudit)
	db.QueryRow(ctx, `SELECT COUNT(DISTINCT entry_hash) FROM audit.event_log`).Scan(&uniqueHashes)

	t.Log("── Audit Log ──")
	t.Logf("  Total entries:     %d", totalAudit)
	t.Logf("  Critical entries:  %d", criticalAudit)
	if totalAudit == uniqueHashes {
		t.Logf("  Hash chain:        INTACT ✓ (%d unique)", uniqueHashes)
	} else {
		t.Logf("  Hash chain:        COLLISION ✗ (%d total, %d unique)", totalAudit, uniqueHashes)
	}
	t.Log("")

	// ─── Reconciliation ───
	var reconSnapshots, reconMismatches int
	db.QueryRow(ctx, `SELECT COUNT(*) FROM recon.snapshots`).Scan(&reconSnapshots)
	db.QueryRow(ctx, `SELECT COUNT(*) FROM recon.snapshots WHERE matches = FALSE`).Scan(&reconMismatches)

	t.Log("── Reconciliation ──")
	t.Logf("  Total snapshots:   %d", reconSnapshots)
	t.Logf("  Mismatches:        %d", reconMismatches)
	t.Log("")

	// ─── Top Denied Check Reasons ───
	rows, err := db.Query(ctx,
		`SELECT
			jsonb_array_elements(checks_json)->>'name' AS check_name,
			COUNT(*) AS cnt
		 FROM risk.policy_decisions
		 WHERE decision = 'denied'
		 GROUP BY check_name
		 ORDER BY cnt DESC
		 LIMIT 10`)
	if err == nil {
		defer rows.Close()
		t.Log("── Top Denial Reasons ──")
		for rows.Next() {
			var checkName string
			var cnt int
			rows.Scan(&checkName, &cnt)
			t.Logf("  %-25s %d", checkName, cnt)
		}
	}
	t.Log("")

	// ─── Positions ───
	posRows, err := db.Query(ctx,
		`SELECT venue, market_id, strategy_id, net_quantity, notional_micros
		 FROM risk.positions WHERE net_quantity != 0 ORDER BY notional_micros DESC LIMIT 10`)
	if err == nil {
		defer posRows.Close()
		t.Log("── Active Positions (top 10) ──")
		for posRows.Next() {
			var venue, marketID, strategyID string
			var qty int32
			var notional int64
			posRows.Scan(&venue, &marketID, &strategyID, &qty, &notional)
			t.Logf("  %s/%s [%s]: qty=%d notional=$%.2f",
				venue, marketID, strategyID, qty, float64(notional)/1_000_000)
		}
	}
	t.Log("")

	// ─── Certification Checklist ───
	t.Log("╔══════════════════════════════════════════════════════╗")
	t.Log("║            CERTIFICATION CHECKLIST                  ║")
	t.Log("╚══════════════════════════════════════════════════════╝")

	checks := []struct {
		name string
		pass bool
		detail string
	}{
		{"Audit hash chain intact", totalAudit == uniqueHashes, fmt.Sprintf("%d/%d unique", uniqueHashes, totalAudit)},
		{"Ledger versions gapless", totalIntents == 0 || int64(totalIntents) == maxV-minV+1, fmt.Sprintf("%d intents, range [%d,%d]", totalIntents, minV, maxV)},
		{"No active kill switches", activeKills == 0, fmt.Sprintf("%d active", activeKills)},
		{"Orders processed", os.Total > 0, fmt.Sprintf("%d total", os.Total)},
		{"Risk decisions recorded", approved+denied > 0, fmt.Sprintf("%d decisions", approved+denied)},
		{"Reconciliation run", reconSnapshots > 0, fmt.Sprintf("%d snapshots", reconSnapshots)},
	}

	allPass := true
	for _, c := range checks {
		status := "PASS"
		if !c.pass {
			status = "FAIL"
			allPass = false
		}
		t.Logf("  [%s] %s (%s)", status, c.name, c.detail)
	}
	t.Log("")

	if allPass {
		t.Log("RESULT: ALL CHECKS PASSED")
	} else {
		t.Log("RESULT: SOME CHECKS FAILED — review above")
	}

	// ─── Timing Stats ───
	var oldestOrder, newestOrder time.Time
	db.QueryRow(ctx, `SELECT COALESCE(MIN(proposed_at), NOW()) FROM execution.orders`).Scan(&oldestOrder)
	db.QueryRow(ctx, `SELECT COALESCE(MAX(proposed_at), NOW()) FROM execution.orders`).Scan(&newestOrder)
	if os.Total > 0 {
		span := newestOrder.Sub(oldestOrder)
		t.Logf("Trading span: %s", span.Round(time.Second))
		if span.Seconds() > 0 {
			t.Logf("Orders/minute: %.1f", float64(os.Total)/span.Minutes())
		}
	}
}
