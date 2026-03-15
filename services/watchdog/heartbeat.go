package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DeadMansSwitch monitors heartbeats from critical services.
// If a service misses too many heartbeats, it triggers a kill switch.
type DeadMansSwitch struct {
	killMgr   *KillSwitchManager
	db        *pgxpool.Pool
	logger    *slog.Logger
	interval  time.Duration
	graceMultiple int

	mu       sync.Mutex
	lastBeat map[string]time.Time
}

func NewDeadMansSwitch(killMgr *KillSwitchManager, db *pgxpool.Pool, interval time.Duration, graceMultiple int) *DeadMansSwitch {
	return &DeadMansSwitch{
		killMgr:       killMgr,
		db:            db,
		logger:        slog.Default().With("component", "dead-mans-switch"),
		interval:      interval,
		graceMultiple: graceMultiple,
		lastBeat:      make(map[string]time.Time),
	}
}

// RecordHeartbeat records a heartbeat from a service.
func (d *DeadMansSwitch) RecordHeartbeat(ctx context.Context, service, status, detail string) {
	now := time.Now().UTC()
	d.mu.Lock()
	d.lastBeat[service] = now
	d.mu.Unlock()

	// Persist to DB for startup recovery
	_, err := d.db.Exec(ctx,
		`INSERT INTO watchdog.heartbeats (service_name, last_heartbeat_at, status, detail)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (service_name) DO UPDATE SET
		   last_heartbeat_at = $2, status = $3, detail = $4`,
		service, now, status, detail,
	)
	if err != nil {
		d.logger.Error("failed to persist heartbeat", "service", service, "error", err)
	}
}

// Monitor runs continuously, checking that critical services are alive.
// Call this in a goroutine.
func (d *DeadMansSwitch) Monitor(ctx context.Context) {
	criticalServices := []string{"execution-engine", "risk-engine"}
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.mu.Lock()
			for _, svc := range criticalServices {
				last, exists := d.lastBeat[svc]
				deadline := time.Duration(d.graceMultiple) * d.interval
				if !exists || time.Since(last) > deadline {
					d.mu.Unlock()
					d.logger.Warn("heartbeat missed", "service", svc,
						"last_seen", last, "deadline", deadline)
					d.killMgr.Trigger(ctx, LevelCancelOnly, "global",
						fmt.Sprintf("dead_mans_switch: %s heartbeat missed", svc),
						"watchdog")
					d.mu.Lock()
				}
			}
			d.mu.Unlock()
		}
	}
}

// GetServiceHealth returns the health status of all monitored services.
func (d *DeadMansSwitch) GetServiceHealth() map[string]ServiceHealth {
	d.mu.Lock()
	defer d.mu.Unlock()

	result := make(map[string]ServiceHealth)
	for svc, last := range d.lastBeat {
		healthy := time.Since(last) < time.Duration(d.graceMultiple)*d.interval
		result[svc] = ServiceHealth{
			ServiceName:   svc,
			Healthy:       healthy,
			LastHeartbeat: last,
		}
	}
	return result
}

type ServiceHealth struct {
	ServiceName   string
	Healthy       bool
	LastHeartbeat time.Time
}
