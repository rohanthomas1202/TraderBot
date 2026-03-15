package ledger

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"autonomy-platform/internal/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ledger is an append-only order intent ledger backed by PostgreSQL.
// It is the ground truth for exposure accounting.
type Ledger struct {
	db     *pgxpool.Pool
	logger *slog.Logger
}

func NewLedger(db *pgxpool.Pool) *Ledger {
	return &Ledger{
		db:     db,
		logger: slog.Default().With("component", "ledger"),
	}
}

// Append records a new order intent in the ledger. The version is assigned
// by the database (BIGSERIAL) and is monotonically increasing.
//
// If a record with the same trace_id already exists, the existing record is
// returned (idempotent). The exposure invariant is checked before writing:
// adding this intent must not exceed the approved exposure envelope.
func (l *Ledger) Append(ctx context.Context, intent *OrderIntent, limits ExposureLimits) (*OrderIntent, error) {
	if err := intent.Validate(); err != nil {
		return nil, fmt.Errorf("invalid intent: %w", err)
	}

	// Check for existing intent with same trace_id (idempotency).
	existing, err := l.GetByTraceID(ctx, intent.TraceID)
	if err != nil {
		return nil, fmt.Errorf("idempotency check: %w", err)
	}
	if existing != nil {
		return existing, nil
	}

	// Compute current exposure for this market+side to check invariant.
	exposure, err := l.ComputeExposure(ctx)
	if err != nil {
		return nil, fmt.Errorf("compute exposure: %w", err)
	}

	if err := exposure.CheckInvariant(intent, limits); err != nil {
		return nil, fmt.Errorf("exposure invariant violated: %w", err)
	}

	// Insert into ledger — version assigned by BIGSERIAL.
	now := time.Now().UTC()
	intentID := uuid.New()
	var version int64

	err = l.db.QueryRow(ctx,
		`INSERT INTO execution.order_intents
			(intent_id, trace_id, approval_hmac, strategy_id, venue, market_id,
			 side, quantity, price_micros, notional_micros, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 RETURNING version`,
		intentID, intent.TraceID, intent.ApprovalHMAC, intent.StrategyID,
		intent.Venue, intent.MarketID, string(intent.Side),
		intent.Quantity, intent.PriceMicros, intent.NotionalMicros,
		string(IntentPending), now, now,
	).Scan(&version)
	if err != nil {
		return nil, fmt.Errorf("insert intent: %w", err)
	}

	intent.IntentID = intentID
	intent.Version = version
	intent.Status = IntentPending
	intent.CreatedAt = now
	intent.UpdatedAt = now

	l.logger.Info("intent appended",
		"intent_id", intentID,
		"version", version,
		"trace_id", intent.TraceID,
		"market_id", intent.MarketID,
		"side", intent.Side,
		"quantity", intent.Quantity,
	)

	return intent, nil
}

// UpdateStatus transitions an intent to a new status.
// Only valid transitions from outstanding → terminal (or pending → open) are allowed.
func (l *Ledger) UpdateStatus(ctx context.Context, intentID uuid.UUID, newStatus IntentStatus) error {
	now := time.Now().UTC()

	tag, err := l.db.Exec(ctx,
		`UPDATE execution.order_intents
		 SET status = $1, updated_at = $2
		 WHERE intent_id = $3
		   AND NOT (status IN ('filled', 'cancelled', 'rejected', 'expired'))`,
		string(newStatus), now, intentID,
	)
	if err != nil {
		return fmt.Errorf("update intent status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("intent %s not found or already in terminal state", intentID)
	}
	return nil
}

// GetByTraceID returns the intent with the given trace_id, or nil if not found.
func (l *Ledger) GetByTraceID(ctx context.Context, traceID string) (*OrderIntent, error) {
	row := l.db.QueryRow(ctx,
		`SELECT intent_id, version, trace_id, approval_hmac, strategy_id, venue, market_id,
		        side, quantity, price_micros, notional_micros, status, created_at, updated_at
		 FROM execution.order_intents
		 WHERE trace_id = $1`, traceID)

	return scanIntent(row)
}

// GetByID returns the intent with the given intent_id, or nil if not found.
func (l *Ledger) GetByID(ctx context.Context, intentID uuid.UUID) (*OrderIntent, error) {
	row := l.db.QueryRow(ctx,
		`SELECT intent_id, version, trace_id, approval_hmac, strategy_id, venue, market_id,
		        side, quantity, price_micros, notional_micros, status, created_at, updated_at
		 FROM execution.order_intents
		 WHERE intent_id = $1`, intentID)

	return scanIntent(row)
}

// ReplayAll reads the full ledger in version order and returns all intents.
// Used to reconstruct exposure state from scratch.
func (l *Ledger) ReplayAll(ctx context.Context) ([]*OrderIntent, error) {
	rows, err := l.db.Query(ctx,
		`SELECT intent_id, version, trace_id, approval_hmac, strategy_id, venue, market_id,
		        side, quantity, price_micros, notional_micros, status, created_at, updated_at
		 FROM execution.order_intents
		 ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("replay query: %w", err)
	}
	defer rows.Close()

	var intents []*OrderIntent
	for rows.Next() {
		intent := &OrderIntent{}
		var side, status string
		if err := rows.Scan(
			&intent.IntentID, &intent.Version, &intent.TraceID, &intent.ApprovalHMAC,
			&intent.StrategyID, &intent.Venue, &intent.MarketID,
			&side, &intent.Quantity, &intent.PriceMicros, &intent.NotionalMicros,
			&status, &intent.CreatedAt, &intent.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan intent: %w", err)
		}
		intent.Side = models.Side(side)
		intent.Status = IntentStatus(status)
		intents = append(intents, intent)
	}
	return intents, rows.Err()
}

// ComputeExposure calculates the current exposure state from outstanding intents.
func (l *Ledger) ComputeExposure(ctx context.Context) (*ExposureState, error) {
	rows, err := l.db.Query(ctx,
		`SELECT market_id, side, SUM(quantity), SUM(notional_micros)
		 FROM execution.order_intents
		 WHERE status IN ('pending', 'open')
		 GROUP BY market_id, side`)
	if err != nil {
		return nil, fmt.Errorf("exposure query: %w", err)
	}
	defer rows.Close()

	state := NewExposureState()
	for rows.Next() {
		var marketID, side string
		var qty int64
		var notional int64
		if err := rows.Scan(&marketID, &side, &qty, &notional); err != nil {
			return nil, fmt.Errorf("scan exposure: %w", err)
		}
		key := MarketSideKey{MarketID: marketID, Side: models.Side(side)}
		state.Outstanding[key] = ExposureEntry{
			Quantity:       int32(qty),
			NotionalMicros: notional,
		}
		state.TotalNotionalMicros += notional
	}
	return state, rows.Err()
}

// scanIntent scans a single row into an OrderIntent. Returns nil if no row found.
func scanIntent(row pgx.Row) (*OrderIntent, error) {
	intent := &OrderIntent{}
	var side, status string
	err := row.Scan(
		&intent.IntentID, &intent.Version, &intent.TraceID, &intent.ApprovalHMAC,
		&intent.StrategyID, &intent.Venue, &intent.MarketID,
		&side, &intent.Quantity, &intent.PriceMicros, &intent.NotionalMicros,
		&status, &intent.CreatedAt, &intent.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	intent.Side = models.Side(side)
	intent.Status = IntentStatus(status)
	return intent, nil
}
