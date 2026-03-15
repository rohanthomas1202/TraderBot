package watchdog

import (
	"context"
	"testing"
)

// --- mock implementations ---

type mockExecControl struct {
	cancelCalled bool
	cancelReason string
	modeSent     string
}

func (m *mockExecControl) CancelAll(_ context.Context, reason, _ string) (int, error) {
	m.cancelCalled = true
	m.cancelReason = reason
	return 3, nil
}

func (m *mockExecControl) SetSystemMode(mode string) {
	m.modeSent = mode
}

type mockRiskControl struct {
	modeSent string
}

func (m *mockRiskControl) SetSystemMode(mode string) {
	m.modeSent = mode
}

// --- severity ranking ---

func TestSeverityRank_Ordering(t *testing.T) {
	if severityRank(LevelSoftPause) != 1 {
		t.Error("soft_pause should rank 1")
	}
	if severityRank(LevelCancelOnly) != 2 {
		t.Error("cancel_only should rank 2")
	}
	if severityRank(LevelFullStop) != 3 {
		t.Error("full_stop should rank 3")
	}
	if severityRank("unknown") != 0 {
		t.Error("unknown should rank 0")
	}
	if !(severityRank(LevelFullStop) > severityRank(LevelCancelOnly) && severityRank(LevelCancelOnly) > severityRank(LevelSoftPause)) {
		t.Error("full_stop > cancel_only > soft_pause must hold")
	}
}

// --- mode recalculation ---

func TestRecalcMode_SingleHalt(t *testing.T) {
	tests := []struct {
		level    KillSwitchLevel
		expected string
	}{
		{LevelSoftPause, "soft_pause"},
		{LevelCancelOnly, "cancel_only"},
		{LevelFullStop, "full_stop"},
	}
	for _, tt := range tests {
		mgr := &KillSwitchManager{
			activeHalts: map[string]*ActiveHalt{
				"global": {Level: tt.level, Scope: "global"},
			},
		}
		mgr.recalcMode()
		if mgr.currentMode != tt.expected {
			t.Errorf("level %s: expected mode %s, got %s", tt.level, tt.expected, mgr.currentMode)
		}
	}
}

func TestRecalcMode_MultipleHalts_HighestWins(t *testing.T) {
	mgr := &KillSwitchManager{
		activeHalts: map[string]*ActiveHalt{
			"strategy:A": {Level: LevelSoftPause, Scope: "strategy:A"},
			"strategy:B": {Level: LevelCancelOnly, Scope: "strategy:B"},
		},
	}
	mgr.recalcMode()
	if mgr.currentMode != "cancel_only" {
		t.Errorf("expected cancel_only, got %s", mgr.currentMode)
	}

	// Add full_stop — should dominate
	mgr.activeHalts["global"] = &ActiveHalt{Level: LevelFullStop, Scope: "global"}
	mgr.recalcMode()
	if mgr.currentMode != "full_stop" {
		t.Errorf("expected full_stop, got %s", mgr.currentMode)
	}
}

func TestRecalcMode_EmptyHalts_Normal(t *testing.T) {
	mgr := &KillSwitchManager{
		activeHalts: make(map[string]*ActiveHalt),
	}
	mgr.recalcMode()
	if mgr.currentMode != "normal" {
		t.Errorf("expected normal, got %s", mgr.currentMode)
	}
}

func TestRecalcMode_AfterRemoval_Downgrades(t *testing.T) {
	mgr := &KillSwitchManager{
		activeHalts: map[string]*ActiveHalt{
			"scope-A": {Level: LevelCancelOnly, Scope: "scope-A"},
			"scope-B": {Level: LevelSoftPause, Scope: "scope-B"},
		},
	}
	mgr.recalcMode()
	if mgr.currentMode != "cancel_only" {
		t.Fatalf("expected cancel_only, got %s", mgr.currentMode)
	}

	delete(mgr.activeHalts, "scope-A")
	mgr.recalcMode()
	if mgr.currentMode != "soft_pause" {
		t.Errorf("expected soft_pause after removing scope-A, got %s", mgr.currentMode)
	}

	delete(mgr.activeHalts, "scope-B")
	mgr.recalcMode()
	if mgr.currentMode != "normal" {
		t.Errorf("expected normal after removing all, got %s", mgr.currentMode)
	}
}

// --- Trigger ---

func newTestManager(exec ExecutionControl, risk RiskControl) *KillSwitchManager {
	return NewKillSwitchManager(nil, exec, risk, nil, nil)
}

func TestTrigger_SetsCorrectMode(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)

	if err := mgr.Trigger(ctx, LevelSoftPause, "global", "test", "operator"); err != nil {
		t.Fatal(err)
	}
	if mgr.GetCurrentMode() != "soft_pause" {
		t.Errorf("expected soft_pause, got %s", mgr.GetCurrentMode())
	}
}

func TestTrigger_CannotDowngrade(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)

	mgr.Trigger(ctx, LevelFullStop, "global", "critical issue", "op1")
	mgr.Trigger(ctx, LevelSoftPause, "global", "minor issue", "op2")

	if mgr.GetCurrentMode() != "full_stop" {
		t.Errorf("expected full_stop (no downgrade), got %s", mgr.GetCurrentMode())
	}
	halts := mgr.GetActiveHalts()
	if len(halts) != 1 {
		t.Fatalf("expected 1 halt, got %d", len(halts))
	}
	if halts[0].Reason != "critical issue" {
		t.Errorf("halt reason should be original 'critical issue', got %q", halts[0].Reason)
	}
}

func TestTrigger_CanUpgrade(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)

	mgr.Trigger(ctx, LevelSoftPause, "global", "minor", "op1")
	mgr.Trigger(ctx, LevelFullStop, "global", "critical", "op2")

	if mgr.GetCurrentMode() != "full_stop" {
		t.Errorf("expected full_stop after upgrade, got %s", mgr.GetCurrentMode())
	}
	halts := mgr.GetActiveHalts()
	if halts[0].Reason != "critical" {
		t.Errorf("halt reason should be upgraded to 'critical', got %q", halts[0].Reason)
	}
}

func TestTrigger_MultipleScopesIndependent(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)

	mgr.Trigger(ctx, LevelSoftPause, "strategy:A", "strat A issue", "op")
	mgr.Trigger(ctx, LevelCancelOnly, "strategy:B", "strat B issue", "op")

	if mgr.GetCurrentMode() != "cancel_only" {
		t.Errorf("expected cancel_only (highest), got %s", mgr.GetCurrentMode())
	}
	if len(mgr.GetActiveHalts()) != 2 {
		t.Errorf("expected 2 active halts, got %d", len(mgr.GetActiveHalts()))
	}
}

func TestTrigger_SameLevelSameScope_NoOp(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)

	mgr.Trigger(ctx, LevelCancelOnly, "global", "first", "op")
	mgr.Trigger(ctx, LevelCancelOnly, "global", "second", "op")

	halts := mgr.GetActiveHalts()
	if halts[0].Reason != "first" {
		t.Errorf("same-level trigger should not replace: expected reason 'first', got %q", halts[0].Reason)
	}
}

// --- Trigger with exec/risk mocks ---

func TestTrigger_CancelOnly_CallsCancelAll(t *testing.T) {
	ctx := context.Background()
	exec := &mockExecControl{}
	risk := &mockRiskControl{}
	mgr := newTestManager(exec, risk)

	mgr.Trigger(ctx, LevelCancelOnly, "global", "test", "watchdog")

	if !exec.cancelCalled {
		t.Error("expected CancelAll to be called for cancel_only")
	}
	if exec.modeSent != "cancel_only" {
		t.Errorf("expected SetSystemMode(cancel_only), got %q", exec.modeSent)
	}
	if risk.modeSent != "cancel_only" {
		t.Errorf("expected risk SetSystemMode(cancel_only), got %q", risk.modeSent)
	}
}

func TestTrigger_FullStop_CallsCancelAll(t *testing.T) {
	ctx := context.Background()
	exec := &mockExecControl{}
	mgr := newTestManager(exec, nil)

	mgr.Trigger(ctx, LevelFullStop, "global", "test", "watchdog")

	if !exec.cancelCalled {
		t.Error("expected CancelAll to be called for full_stop")
	}
	if exec.modeSent != "full_stop" {
		t.Errorf("expected SetSystemMode(full_stop), got %q", exec.modeSent)
	}
}

func TestTrigger_SoftPause_DoesNotCancelAll(t *testing.T) {
	ctx := context.Background()
	exec := &mockExecControl{}
	mgr := newTestManager(exec, nil)

	mgr.Trigger(ctx, LevelSoftPause, "global", "test", "watchdog")

	if exec.cancelCalled {
		t.Error("CancelAll should NOT be called for soft_pause")
	}
	if exec.modeSent != "soft_pause" {
		t.Errorf("expected SetSystemMode(soft_pause), got %q", exec.modeSent)
	}
}

// --- Acknowledge ---

func TestAcknowledge_Success(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)

	mgr.Trigger(ctx, LevelCancelOnly, "global", "test", "op")
	if err := mgr.Acknowledge(ctx, "global", "operator", "investigated, false alarm"); err != nil {
		t.Fatal(err)
	}

	halts := mgr.GetActiveHalts()
	if len(halts) != 1 {
		t.Fatalf("expected 1 halt, got %d", len(halts))
	}
	if !halts[0].Acknowledged {
		t.Error("halt should be acknowledged")
	}
	if halts[0].RootCause != "investigated, false alarm" {
		t.Errorf("unexpected root cause: %q", halts[0].RootCause)
	}
}

func TestAcknowledge_NoActiveHalt_Error(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)

	err := mgr.Acknowledge(ctx, "nonexistent", "op", "reason")
	if err == nil {
		t.Fatal("expected error for nonexistent scope")
	}
}

func TestAcknowledge_AlreadyAcked_Error(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)

	mgr.Trigger(ctx, LevelSoftPause, "global", "test", "op")
	mgr.Acknowledge(ctx, "global", "op", "reason")

	err := mgr.Acknowledge(ctx, "global", "op", "duplicate")
	if err == nil {
		t.Fatal("expected error for double acknowledgment")
	}
}

// --- Resume ---

func TestResume_WithoutAck_Error(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)

	mgr.Trigger(ctx, LevelSoftPause, "global", "test", "op")

	err := mgr.Resume(ctx, "global", "op")
	if err == nil {
		t.Fatal("expected error: must acknowledge before resuming")
	}
}

func TestResume_AfterAck_Success(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)

	mgr.Trigger(ctx, LevelCancelOnly, "global", "test", "op")
	mgr.Acknowledge(ctx, "global", "op", "root cause")
	if err := mgr.Resume(ctx, "global", "op"); err != nil {
		t.Fatal(err)
	}

	if mgr.GetCurrentMode() != "normal" {
		t.Errorf("expected normal after resume, got %s", mgr.GetCurrentMode())
	}
	if len(mgr.GetActiveHalts()) != 0 {
		t.Errorf("expected no active halts, got %d", len(mgr.GetActiveHalts()))
	}
}

func TestResume_NoActiveHalt_Error(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)

	err := mgr.Resume(ctx, "nonexistent", "op")
	if err == nil {
		t.Fatal("expected error for nonexistent scope")
	}
}

func TestResume_PartialClear_ModeDowngrades(t *testing.T) {
	ctx := context.Background()
	exec := &mockExecControl{}
	risk := &mockRiskControl{}
	mgr := newTestManager(exec, risk)

	mgr.Trigger(ctx, LevelFullStop, "scope-A", "critical", "op")
	mgr.Trigger(ctx, LevelSoftPause, "scope-B", "minor", "op")

	if mgr.GetCurrentMode() != "full_stop" {
		t.Fatalf("expected full_stop, got %s", mgr.GetCurrentMode())
	}

	// Ack and resume scope-A
	mgr.Acknowledge(ctx, "scope-A", "op", "fixed")
	mgr.Resume(ctx, "scope-A", "op")

	if mgr.GetCurrentMode() != "soft_pause" {
		t.Errorf("expected soft_pause after partial resume, got %s", mgr.GetCurrentMode())
	}
	if exec.modeSent != "soft_pause" {
		t.Errorf("expected exec mode soft_pause, got %q", exec.modeSent)
	}
	if risk.modeSent != "soft_pause" {
		t.Errorf("expected risk mode soft_pause, got %q", risk.modeSent)
	}
}

// --- Full lifecycle ---

func TestFullLifecycle_TriggerAckResume(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(nil, nil)

	// Start normal
	if mgr.GetCurrentMode() != "normal" {
		t.Fatalf("expected normal, got %s", mgr.GetCurrentMode())
	}

	// Trigger
	mgr.Trigger(ctx, LevelCancelOnly, "global", "drawdown breach", "watchdog")
	if mgr.GetCurrentMode() != "cancel_only" {
		t.Fatalf("expected cancel_only, got %s", mgr.GetCurrentMode())
	}

	// Resume without ack fails
	if err := mgr.Resume(ctx, "global", "op"); err == nil {
		t.Fatal("expected resume to fail without ack")
	}

	// Ack
	mgr.Acknowledge(ctx, "global", "op", "false alarm from bad data feed")

	// Resume
	if err := mgr.Resume(ctx, "global", "op"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if mgr.GetCurrentMode() != "normal" {
		t.Fatalf("expected normal after full lifecycle, got %s", mgr.GetCurrentMode())
	}
}
