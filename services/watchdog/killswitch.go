package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"autonomy-platform/internal/audit"
	"autonomy-platform/internal/events"

	"github.com/jackc/pgx/v5/pgxpool"
)

type KillSwitchLevel string

const (
	LevelSoftPause  KillSwitchLevel = "soft_pause"
	LevelCancelOnly KillSwitchLevel = "cancel_only"
	LevelFullStop   KillSwitchLevel = "full_stop"
)

type ActiveHalt struct {
	Level        KillSwitchLevel
	Scope        string
	Reason       string
	TriggeredBy  string
	TriggeredAt  time.Time
	Acknowledged bool
	AckedBy      string
	AckedAt      *time.Time
	RootCause    string
	Resumed      bool
}

// ExecutionControl is the interface the kill switch uses to stop trading.
// Implemented by the execution engine.
type ExecutionControl interface {
	CancelAll(ctx context.Context, reason, cancelledBy string) (int, error)
	SetSystemMode(mode string)
}

// RiskControl is the interface to update risk engine mode.
type RiskControl interface {
	SetSystemMode(mode string)
}

type KillSwitchManager struct {
	db        *pgxpool.Pool
	exec      ExecutionControl
	risk      RiskControl
	publisher *events.Publisher
	auditor   *audit.Logger
	logger    *slog.Logger

	mu          sync.RWMutex
	activeHalts map[string]*ActiveHalt // scope → halt
	currentMode string
}

func NewKillSwitchManager(db *pgxpool.Pool, exec ExecutionControl, riskCtrl RiskControl, pub *events.Publisher, aud *audit.Logger) *KillSwitchManager {
	return &KillSwitchManager{
		db:          db,
		exec:        exec,
		risk:        riskCtrl,
		publisher:   pub,
		auditor:     aud,
		logger:      slog.Default().With("component", "kill-switch"),
		activeHalts: make(map[string]*ActiveHalt),
		currentMode: "normal",
	}
}

// LoadActiveHalts recovers halt state from DB on startup.
func (k *KillSwitchManager) LoadActiveHalts(ctx context.Context) error {
	rows, err := k.db.Query(ctx,
		`SELECT level, scope, reason, triggered_by, triggered_at, acknowledged, acknowledged_by, acknowledged_at, root_cause
		 FROM watchdog.kill_switch_events
		 WHERE resumed = FALSE`)
	if err != nil {
		return fmt.Errorf("load halts: %w", err)
	}
	defer rows.Close()

	k.mu.Lock()
	defer k.mu.Unlock()

	for rows.Next() {
		var h ActiveHalt
		var level, ackedBy, rootCause *string
		var ackedAt *time.Time
		if err := rows.Scan(&h.Level, &h.Scope, &h.Reason, &h.TriggeredBy,
			&h.TriggeredAt, &h.Acknowledged, &ackedBy, &ackedAt, &rootCause); err != nil {
			return fmt.Errorf("scan halt: %w", err)
		}
		if ackedBy != nil {
			h.AckedBy = *ackedBy
		}
		h.AckedAt = ackedAt
		if rootCause != nil {
			h.RootCause = *rootCause
		}
		_ = level // already scanned into h.Level
		k.activeHalts[h.Scope] = &h
	}

	k.recalcMode()

	if k.currentMode != "normal" {
		k.logger.Warn("starting with active halts", "mode", k.currentMode, "halt_count", len(k.activeHalts))
	}
	return nil
}

// Trigger activates a kill switch.
func (k *KillSwitchManager) Trigger(ctx context.Context, level KillSwitchLevel, scope, reason, triggeredBy string) error {
	k.mu.Lock()

	// Don't downgrade an existing halt on the same scope
	if existing, exists := k.activeHalts[scope]; exists {
		if severityRank(existing.Level) >= severityRank(level) {
			k.mu.Unlock()
			k.logger.Info("kill switch already active at same or higher level",
				"scope", scope, "existing", existing.Level, "requested", level)
			return nil
		}
	}

	halt := &ActiveHalt{
		Level:       level,
		Scope:       scope,
		Reason:      reason,
		TriggeredBy: triggeredBy,
		TriggeredAt: time.Now().UTC(),
	}
	k.activeHalts[scope] = halt
	k.recalcMode()
	mode := k.currentMode
	k.mu.Unlock()

	// Persist to DB first
	if k.db != nil {
		_, err := k.db.Exec(ctx,
			`INSERT INTO watchdog.kill_switch_events (level, scope, reason, triggered_by, triggered_at)
			 VALUES ($1, $2, $3, $4, $5)`,
			string(level), scope, reason, triggeredBy, halt.TriggeredAt,
		)
		if err != nil {
			k.logger.Error("failed to persist kill switch event", "error", err)
			// Continue anyway — the in-memory state is already updated
		}
	}

	// Execute kill switch actions
	switch level {
	case LevelFullStop, LevelCancelOnly:
		if k.exec != nil {
			cancelled, err := k.exec.CancelAll(ctx, "kill_switch:"+reason, triggeredBy)
			if err != nil {
				k.logger.Error("cancel-all failed during kill switch", "error", err)
			} else {
				k.logger.Info("cancelled orders", "count", cancelled)
			}
		}
	case LevelSoftPause:
		// No active cancellation — risk engine denies new orders based on mode
	}

	// Update execution and risk engine modes
	if k.exec != nil {
		k.exec.SetSystemMode(mode)
	}
	if k.risk != nil {
		k.risk.SetSystemMode(mode)
	}

	// Publish event
	if k.publisher != nil {
		k.publisher.Publish(events.SubjectKillActivated+"."+scope, events.KillSwitchEvent{
		Level:       string(level),
		Scope:       scope,
		Reason:      reason,
		TriggeredBy: triggeredBy,
		Timestamp:   halt.TriggeredAt,
		})
	}

	// Audit log
	if k.auditor != nil {
		k.auditor.LogCritical(ctx, "kill_switch.activated", "", map[string]interface{}{
		"level":        string(level),
		"scope":        scope,
		"reason":       reason,
		"triggered_by": triggeredBy,
		})
	}

	k.logger.Warn("KILL SWITCH ACTIVATED",
		"level", level,
		"scope", scope,
		"reason", reason,
		"triggered_by", triggeredBy,
		"system_mode", mode,
	)

	return nil
}

// Acknowledge marks a halt as acknowledged. Required before resume.
func (k *KillSwitchManager) Acknowledge(ctx context.Context, scope, ackedBy, rootCause string) error {
	k.mu.Lock()
	halt, exists := k.activeHalts[scope]
	if !exists {
		k.mu.Unlock()
		return fmt.Errorf("no active halt on scope %s", scope)
	}
	if halt.Acknowledged {
		k.mu.Unlock()
		return fmt.Errorf("halt on scope %s already acknowledged", scope)
	}
	now := time.Now().UTC()
	halt.Acknowledged = true
	halt.AckedBy = ackedBy
	halt.AckedAt = &now
	halt.RootCause = rootCause
	k.mu.Unlock()

	if k.db != nil {
		_, err := k.db.Exec(ctx,
			`UPDATE watchdog.kill_switch_events
			 SET acknowledged = TRUE, acknowledged_by = $1, acknowledged_at = $2, root_cause = $3
			 WHERE scope = $4 AND resumed = FALSE AND acknowledged = FALSE`,
			ackedBy, now, rootCause, scope,
		)
		if err != nil {
			k.logger.Error("failed to persist acknowledgment", "error", err)
		}
	}

	if k.auditor != nil {
		k.auditor.LogInfo(ctx, "kill_switch.acknowledged", "", map[string]interface{}{
			"scope":      scope,
			"acked_by":   ackedBy,
			"root_cause": rootCause,
		})
	}

	return nil
}

// Resume clears a halt and potentially returns to normal mode.
func (k *KillSwitchManager) Resume(ctx context.Context, scope, resumedBy string) error {
	k.mu.Lock()
	halt, exists := k.activeHalts[scope]
	if !exists {
		k.mu.Unlock()
		return fmt.Errorf("no active halt on scope %s", scope)
	}
	if !halt.Acknowledged {
		k.mu.Unlock()
		return fmt.Errorf("halt on scope %s must be acknowledged before resuming", scope)
	}

	delete(k.activeHalts, scope)
	k.recalcMode()
	mode := k.currentMode
	k.mu.Unlock()

	if k.db != nil {
		_, err := k.db.Exec(ctx,
			`UPDATE watchdog.kill_switch_events
			 SET resumed = TRUE, resumed_by = $1, resumed_at = $2
			 WHERE scope = $3 AND resumed = FALSE`,
			resumedBy, time.Now().UTC(), scope,
		)
		if err != nil {
			k.logger.Error("failed to persist resume", "error", err)
		}
	}

	// Update service modes
	if k.exec != nil {
		k.exec.SetSystemMode(mode)
	}
	if k.risk != nil {
		k.risk.SetSystemMode(mode)
	}

	if k.auditor != nil {
		k.auditor.LogInfo(ctx, "kill_switch.resumed", "", map[string]interface{}{
			"scope":      scope,
			"resumed_by": resumedBy,
			"new_mode":   mode,
		})
	}

	k.logger.Info("kill switch cleared", "scope", scope, "resumed_by", resumedBy, "new_mode", mode)
	return nil
}

// GetCurrentMode returns the current system mode.
func (k *KillSwitchManager) GetCurrentMode() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.currentMode
}

// GetActiveHalts returns all active halts.
func (k *KillSwitchManager) GetActiveHalts() []ActiveHalt {
	k.mu.RLock()
	defer k.mu.RUnlock()
	halts := make([]ActiveHalt, 0, len(k.activeHalts))
	for _, h := range k.activeHalts {
		halts = append(halts, *h)
	}
	return halts
}

func (k *KillSwitchManager) recalcMode() {
	// Must be called with k.mu held
	mode := "normal"
	for _, halt := range k.activeHalts {
		switch halt.Level {
		case LevelFullStop:
			k.currentMode = "full_stop"
			return
		case LevelCancelOnly:
			if mode != "full_stop" {
				mode = "cancel_only"
			}
		case LevelSoftPause:
			if mode == "normal" {
				mode = "soft_pause"
			}
		}
	}
	k.currentMode = mode
}

func severityRank(level KillSwitchLevel) int {
	switch level {
	case LevelSoftPause:
		return 1
	case LevelCancelOnly:
		return 2
	case LevelFullStop:
		return 3
	default:
		return 0
	}
}
