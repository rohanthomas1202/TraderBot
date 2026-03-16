package backtest

import (
	"context"
	"fmt"
	"sort"
	"time"

	"autonomy-platform/internal/models"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Snapshot represents a single market data point from the database.
type Snapshot struct {
	Venue         string
	MarketID      string
	CapturedAt    time.Time
	BidPriceMicros int64
	AskPriceMicros int64
	BidDepth      int
	AskDepth      int
	Volume        int64
}

// Tick represents all market snapshots at approximately the same time.
type Tick struct {
	Timestamp time.Time
	Data      map[string]*models.MarketData // venue:market_id → data
}

// LoadSnapshots loads historical market data from the database for the given date range.
func LoadSnapshots(ctx context.Context, db *pgxpool.Pool, venue string, from, to time.Time) ([]Snapshot, error) {
	rows, err := db.Query(ctx,
		`SELECT venue, market_id, captured_at, best_bid_micros, best_ask_micros,
		        bid_depth, ask_depth, volume
		 FROM backtest.market_snapshots
		 WHERE venue = $1 AND captured_at >= $2 AND captured_at < $3
		 ORDER BY captured_at`,
		venue, from, to)
	if err != nil {
		return nil, fmt.Errorf("load snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []Snapshot
	for rows.Next() {
		var s Snapshot
		if err := rows.Scan(&s.Venue, &s.MarketID, &s.CapturedAt,
			&s.BidPriceMicros, &s.AskPriceMicros, &s.BidDepth, &s.AskDepth, &s.Volume); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, nil
}

// GroupByTimestamp groups snapshots into ticks with the given time tolerance.
// Snapshots within tolerance of each other are considered simultaneous.
func GroupByTimestamp(snapshots []Snapshot, tolerance time.Duration) []Tick {
	if len(snapshots) == 0 {
		return nil
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].CapturedAt.Before(snapshots[j].CapturedAt)
	})

	var ticks []Tick
	currentTick := Tick{
		Timestamp: snapshots[0].CapturedAt,
		Data:      make(map[string]*models.MarketData),
	}

	for _, s := range snapshots {
		if s.CapturedAt.Sub(currentTick.Timestamp) > tolerance {
			ticks = append(ticks, currentTick)
			currentTick = Tick{
				Timestamp: s.CapturedAt,
				Data:      make(map[string]*models.MarketData),
			}
		}
		key := s.Venue + ":" + s.MarketID
		currentTick.Data[key] = &models.MarketData{
			Venue:         s.Venue,
			MarketID:      s.MarketID,
			BidPriceMicros: s.BidPriceMicros,
			AskPriceMicros: s.AskPriceMicros,
			Volume24h:     float64(s.Volume),
			UpdatedAt:     s.CapturedAt,
		}
	}
	ticks = append(ticks, currentTick)
	return ticks
}
