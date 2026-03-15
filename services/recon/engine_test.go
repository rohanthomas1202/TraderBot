package recon

import (
	"context"
	"testing"
)

// mockKillSwitch records Trigger calls for testing.
type mockKillSwitch struct {
	triggered bool
	level     string
	reason    string
}

func (m *mockKillSwitch) Trigger(_ context.Context, level string, _, reason, _ string) error {
	m.triggered = true
	m.level = level
	m.reason = reason
	return nil
}

func TestCompareResult_CriticalSeverity(t *testing.T) {
	result := &CompareResult{
		SnapshotType: "positions",
		Matches:      false,
		Severity:     "critical",
		Mismatches: []Mismatch{
			{Field: "position", Key: "BTC-USD", Internal: "10", Exchange: "8"},
		},
	}

	if result.Matches {
		t.Error("expected matches=false")
	}
	if result.Severity != "critical" {
		t.Errorf("expected severity=critical, got %s", result.Severity)
	}
}

func TestCompareResult_InfoSeverity(t *testing.T) {
	result := &CompareResult{
		SnapshotType: "balance",
		Matches:      true,
		Severity:     "info",
	}

	if !result.Matches {
		t.Error("expected matches=true")
	}
	if result.Severity != "info" {
		t.Errorf("expected severity=info, got %s", result.Severity)
	}
}
