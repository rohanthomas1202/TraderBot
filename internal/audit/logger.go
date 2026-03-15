package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Entry is the universal audit record. Every entry is hash-chained
// to the previous for tamper detection.
type Entry struct {
	ID           string          `json:"id"`
	Timestamp    time.Time       `json:"timestamp"`
	Service      string          `json:"service"`
	EventType    string          `json:"event_type"`
	TraceID      string          `json:"trace_id,omitempty"`
	Severity     string          `json:"severity"`
	Payload      json.RawMessage `json:"payload"`
	PreviousHash string          `json:"previous_hash"`
	EntryHash    string          `json:"entry_hash"`
}

// Logger provides hash-chained audit logging to PostgreSQL.
// Phase 1: writes to audit.event_log table.
// Phase 3+: will also write to S3 Object Lock.
type Logger struct {
	service  string
	db       *pgxpool.Pool
	mu       sync.Mutex
	lastHash string
	logger   *slog.Logger
}

func NewLogger(service string, db *pgxpool.Pool) *Logger {
	return &Logger{
		service:  service,
		db:       db,
		lastHash: "genesis",
		logger:   slog.Default().With("component", "audit"),
	}
}

// Log writes an audit entry. This is the primary interface.
// payload is any struct that will be JSON-marshaled.
func (l *Logger) Log(ctx context.Context, eventType, traceID, severity string, payload interface{}) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal audit payload: %w", err)
	}

	l.mu.Lock()
	entry := Entry{
		ID:           uuid.New().String(),
		Timestamp:    time.Now().UTC(),
		Service:      l.service,
		EventType:    eventType,
		TraceID:      traceID,
		Severity:     severity,
		Payload:      payloadBytes,
		PreviousHash: l.lastHash,
	}

	// Compute hash chain
	hashInput, _ := json.Marshal(struct {
		ID           string          `json:"id"`
		Timestamp    time.Time       `json:"ts"`
		Service      string          `json:"svc"`
		EventType    string          `json:"et"`
		TraceID      string          `json:"tid"`
		Payload      json.RawMessage `json:"p"`
		PreviousHash string          `json:"ph"`
	}{entry.ID, entry.Timestamp, entry.Service, entry.EventType, entry.TraceID, entry.Payload, entry.PreviousHash})

	hash := sha256.Sum256(hashInput)
	entry.EntryHash = hex.EncodeToString(hash[:])
	l.lastHash = entry.EntryHash
	l.mu.Unlock()

	// Write to database
	_, err = l.db.Exec(ctx,
		`INSERT INTO audit.event_log (id, timestamp, service, event_type, trace_id, severity, payload, previous_hash, entry_hash)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		entry.ID, entry.Timestamp, entry.Service, entry.EventType,
		nilIfEmpty(entry.TraceID), entry.Severity, entry.Payload,
		entry.PreviousHash, entry.EntryHash,
	)
	if err != nil {
		// Audit write failure is serious but should not crash the service.
		// Log locally and continue. The hash chain will have a gap, which
		// is detectable during forensic review.
		l.logger.Error("audit write failed",
			"event_type", eventType,
			"trace_id", traceID,
			"error", err,
		)
		return fmt.Errorf("audit write: %w", err)
	}

	l.logger.Debug("audit logged",
		"event_type", eventType,
		"trace_id", traceID,
		"severity", severity,
	)
	return nil
}

// LogInfo is a convenience wrapper for info-level events.
func (l *Logger) LogInfo(ctx context.Context, eventType, traceID string, payload interface{}) {
	_ = l.Log(ctx, eventType, traceID, "info", payload)
}

// LogWarn is a convenience wrapper for warning-level events.
func (l *Logger) LogWarn(ctx context.Context, eventType, traceID string, payload interface{}) {
	_ = l.Log(ctx, eventType, traceID, "warn", payload)
}

// LogCritical is a convenience wrapper for critical-level events.
func (l *Logger) LogCritical(ctx context.Context, eventType, traceID string, payload interface{}) {
	_ = l.Log(ctx, eventType, traceID, "critical", payload)
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
