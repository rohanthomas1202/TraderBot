//go:build integration

package integration

import (
	"context"
	"testing"

	"autonomy-platform/internal/audit"
)

// TestAuditLog_HashChainIntegrity verifies that audit log entries are
// hash-chained and that tampering would be detectable.
func TestAuditLog_HashChainIntegrity(t *testing.T) {
	db, _, _ := setupTestEnv(t)
	ctx := context.Background()

	logger := audit.NewLogger("test-audit", db)

	// Write several audit entries
	entries := []struct {
		eventType string
		traceID   string
		payload   interface{}
	}{
		{"test.event.first", "trace-001", map[string]string{"action": "first entry"}},
		{"test.event.second", "trace-002", map[string]string{"action": "second entry"}},
		{"test.event.third", "trace-003", map[string]string{"action": "third entry"}},
	}

	for _, e := range entries {
		err := logger.Log(ctx, e.eventType, e.traceID, "info", e.payload)
		if err != nil {
			t.Fatalf("log entry %s: %v", e.eventType, err)
		}
	}

	// Read back and verify hash chain
	rows, err := db.Query(ctx,
		`SELECT entry_hash, previous_hash, event_type
		 FROM audit.event_log
		 WHERE service = 'test-audit'
		 ORDER BY timestamp ASC`)
	if err != nil {
		t.Fatalf("query audit log: %v", err)
	}
	defer rows.Close()

	var prevHash string
	count := 0
	for rows.Next() {
		var entryHash, previousHash, eventType string
		if err := rows.Scan(&entryHash, &previousHash, &eventType); err != nil {
			t.Fatalf("scan: %v", err)
		}

		if count == 0 {
			// First entry should reference "genesis"
			if previousHash != "genesis" {
				t.Errorf("first entry previous_hash should be 'genesis', got %s", previousHash)
			}
		} else {
			// Subsequent entries should reference the previous entry's hash
			if previousHash != prevHash {
				t.Errorf("broken hash chain at entry %d: expected previous=%s, got %s",
					count, prevHash, previousHash)
			}
		}

		if entryHash == "" {
			t.Errorf("entry %d has empty hash", count)
		}

		prevHash = entryHash
		count++
		t.Logf("Entry %d: type=%s hash=%s...%s", count, eventType, entryHash[:8], entryHash[len(entryHash)-8:])
	}

	if count != len(entries) {
		t.Errorf("expected %d audit entries, got %d", len(entries), count)
	}
}

// TestAuditLog_CriticalEventsLogged verifies that kill switch events
// create critical-severity audit entries.
func TestAuditLog_CriticalEventsLogged(t *testing.T) {
	db, _, _ := setupTestEnv(t)
	ctx := context.Background()

	logger := audit.NewLogger("test-critical", db)

	// Log a critical event
	logger.LogCritical(ctx, "kill_switch.activated", "trace-critical", map[string]string{
		"level":  "cancel_only",
		"scope":  "global",
		"reason": "test",
	})

	// Verify it's stored with critical severity
	var severity string
	err := db.QueryRow(ctx,
		`SELECT severity FROM audit.event_log
		 WHERE service = 'test-critical' AND event_type = 'kill_switch.activated'
		 LIMIT 1`).Scan(&severity)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if severity != "critical" {
		t.Errorf("expected critical severity, got %s", severity)
	}
}
