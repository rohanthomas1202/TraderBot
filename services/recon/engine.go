package recon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"autonomy-platform/internal/audit"

	"github.com/jackc/pgx/v5/pgxpool"
)

// KillSwitchTrigger is the interface for triggering kill switches on critical mismatches.
type KillSwitchTrigger interface {
	Trigger(ctx context.Context, level string, scope, reason, triggeredBy string) error
}

// Engine runs periodic reconciliation checks and persists results.
type Engine struct {
	db         *pgxpool.Pool
	comparator *Comparator
	killSwitch KillSwitchTrigger
	auditor    *audit.Logger
	logger     *slog.Logger
	interval   time.Duration
}

func NewEngine(db *pgxpool.Pool, comparator *Comparator, killSwitch KillSwitchTrigger, auditor *audit.Logger, interval time.Duration) *Engine {
	return &Engine{
		db:         db,
		comparator: comparator,
		killSwitch: killSwitch,
		auditor:    auditor,
		logger:     slog.Default().With("component", "recon-engine"),
		interval:   interval,
	}
}

// Run starts the periodic reconciliation loop. It blocks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	e.logger.Info("reconciliation engine started", "interval", e.interval)

	for {
		select {
		case <-ctx.Done():
			e.logger.Info("reconciliation engine stopped")
			return
		case <-ticker.C:
			if err := e.RunOnce(ctx); err != nil {
				e.logger.Error("reconciliation cycle failed", "error", err)
			}
		}
	}
}

// RunOnce executes a single reconciliation cycle: compare all aspects,
// persist snapshots, and trigger kill switch on critical mismatches.
func (e *Engine) RunOnce(ctx context.Context) error {
	start := time.Now()

	// Run all comparisons
	posResult, posErr := e.comparator.ComparePositions(ctx)
	orderResult, orderErr := e.comparator.CompareOrders(ctx)
	balResult, balErr := e.comparator.CompareBalance(ctx)

	results := []*CompareResult{posResult, orderResult, balResult}
	errors := []error{posErr, orderErr, balErr}

	var criticalMismatches []string

	for i, result := range results {
		if errors[i] != nil {
			e.logger.Error("comparison failed", "type", result, "error", errors[i])
			continue
		}
		if result == nil {
			continue
		}

		// Persist snapshot
		if err := e.persistSnapshot(ctx, result); err != nil {
			e.logger.Error("failed to persist snapshot", "type", result.SnapshotType, "error", err)
		}

		if !result.Matches {
			e.logger.Warn("reconciliation mismatch detected",
				"type", result.SnapshotType,
				"severity", result.Severity,
				"mismatch_count", len(result.Mismatches),
			)

			if result.Severity == "critical" {
				criticalMismatches = append(criticalMismatches, result.SnapshotType)
			}
		}
	}

	duration := time.Since(start)
	e.logger.Debug("reconciliation cycle complete", "duration", duration, "critical_mismatches", len(criticalMismatches))

	// Auto-trigger kill switch on critical mismatches
	if len(criticalMismatches) > 0 {
		reason := fmt.Sprintf("reconciliation critical mismatch: %v", criticalMismatches)
		e.logger.Error("CRITICAL MISMATCH — triggering kill switch", "types", criticalMismatches)

		if e.auditor != nil {
			e.auditor.LogCritical(ctx, "recon.critical_mismatch", "", map[string]interface{}{
				"mismatch_types": criticalMismatches,
			})
		}

		if e.killSwitch != nil {
			if err := e.killSwitch.Trigger(ctx, "cancel_only", "global", reason, "recon-engine"); err != nil {
				e.logger.Error("failed to trigger kill switch on mismatch", "error", err)
			}
		}
	}

	return nil
}

// RunStartupCheck performs a single reconciliation pass and returns whether
// the state is consistent. Used as a startup gate before allowing trading.
func (e *Engine) RunStartupCheck(ctx context.Context) (bool, error) {
	posResult, err := e.comparator.ComparePositions(ctx)
	if err != nil {
		return false, fmt.Errorf("startup position check: %w", err)
	}

	orderResult, err := e.comparator.CompareOrders(ctx)
	if err != nil {
		return false, fmt.Errorf("startup order check: %w", err)
	}

	allMatch := true
	for _, result := range []*CompareResult{posResult, orderResult} {
		if err := e.persistSnapshot(ctx, result); err != nil {
			e.logger.Error("failed to persist startup snapshot", "type", result.SnapshotType, "error", err)
		}
		if !result.Matches {
			allMatch = false
			e.logger.Warn("startup reconciliation mismatch",
				"type", result.SnapshotType,
				"severity", result.Severity,
				"mismatches", result.Mismatches,
			)
		}
	}

	if allMatch {
		e.logger.Info("startup reconciliation passed — state is consistent")
	} else {
		e.logger.Warn("startup reconciliation found mismatches — review before trading")
	}

	return allMatch, nil
}

func (e *Engine) persistSnapshot(ctx context.Context, result *CompareResult) error {
	internalJSON, err := json.Marshal(result.InternalState)
	if err != nil {
		return fmt.Errorf("marshal internal state: %w", err)
	}
	exchangeJSON, err := json.Marshal(result.ExchangeState)
	if err != nil {
		return fmt.Errorf("marshal exchange state: %w", err)
	}

	var mismatchJSON []byte
	if len(result.Mismatches) > 0 {
		mismatchJSON, err = json.Marshal(result.Mismatches)
		if err != nil {
			return fmt.Errorf("marshal mismatches: %w", err)
		}
	}

	_, err = e.db.Exec(ctx,
		`INSERT INTO recon.snapshots
			(venue, snapshot_type, matches, internal_state, exchange_state, mismatches, severity)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		"mock", result.SnapshotType, result.Matches,
		internalJSON, exchangeJSON, mismatchJSON, result.Severity,
	)
	if err != nil {
		return fmt.Errorf("insert snapshot: %w", err)
	}
	return nil
}
