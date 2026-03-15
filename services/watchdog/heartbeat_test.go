package watchdog

import (
	"context"
	"testing"
	"time"
)

func TestGetServiceHealth_NoHeartbeats(t *testing.T) {
	dms := NewDeadMansSwitch(nil, nil, 5*time.Second, 3)
	health := dms.GetServiceHealth()
	if len(health) != 0 {
		t.Errorf("expected empty health map, got %d entries", len(health))
	}
}

func TestGetServiceHealth_RecentHeartbeat_Healthy(t *testing.T) {
	dms := &DeadMansSwitch{
		interval:      5 * time.Second,
		graceMultiple: 3,
		lastBeat: map[string]time.Time{
			"execution-engine": time.Now(),
		},
	}
	health := dms.GetServiceHealth()
	svc, ok := health["execution-engine"]
	if !ok {
		t.Fatal("expected execution-engine in health map")
	}
	if !svc.Healthy {
		t.Error("expected healthy for recent heartbeat")
	}
}

func TestGetServiceHealth_StaleHeartbeat_Unhealthy(t *testing.T) {
	dms := &DeadMansSwitch{
		interval:      5 * time.Second,
		graceMultiple: 3, // deadline = 15s
		lastBeat: map[string]time.Time{
			"risk-engine": time.Now().Add(-30 * time.Second), // 30s ago > 15s deadline
		},
	}
	health := dms.GetServiceHealth()
	svc := health["risk-engine"]
	if svc.Healthy {
		t.Error("expected unhealthy for stale heartbeat (30s > 15s deadline)")
	}
}

func TestRecordHeartbeat_UpdatesLastBeat(t *testing.T) {
	ctx := context.Background()
	dms := NewDeadMansSwitch(nil, nil, 5*time.Second, 3)

	dms.RecordHeartbeat(ctx, "test-service", "healthy", "all good")

	health := dms.GetServiceHealth()
	svc, ok := health["test-service"]
	if !ok {
		t.Fatal("expected test-service in health map after heartbeat")
	}
	if !svc.Healthy {
		t.Error("expected healthy immediately after heartbeat")
	}
	if svc.ServiceName != "test-service" {
		t.Errorf("expected ServiceName 'test-service', got %q", svc.ServiceName)
	}
}

func TestMonitor_TriggersKillOnMissedHeartbeat(t *testing.T) {
	// Create a kill switch manager that records triggers
	mgr := NewKillSwitchManager(nil, nil, nil, nil, nil)

	// Very short interval so the monitor fires quickly
	dms := NewDeadMansSwitch(mgr, nil, 10*time.Millisecond, 1)

	ctx, cancel := context.WithCancel(context.Background())

	// Start monitor — it should trigger kill switch because no heartbeats exist
	go dms.Monitor(ctx)

	// Wait enough for at least one check cycle
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Verify kill switch was triggered
	mode := mgr.GetCurrentMode()
	if mode != "cancel_only" {
		t.Errorf("expected cancel_only from dead man's switch, got %s", mode)
	}

	halts := mgr.GetActiveHalts()
	if len(halts) == 0 {
		t.Fatal("expected active halt from dead man's switch")
	}

	foundHeartbeatReason := false
	for _, h := range halts {
		if h.TriggeredBy == "watchdog" {
			foundHeartbeatReason = true
		}
	}
	if !foundHeartbeatReason {
		t.Error("expected halt triggered by 'watchdog'")
	}
}
