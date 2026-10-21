package inventory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

type ReplayPosition struct {
	LocationID         string `json:"location_id"`
	SKU                string `json:"sku"`
	MovementCount      int64  `json:"movement_count"`
	ReplayedOnHand     int64  `json:"replayed_on_hand"`
	ReplayedValueCents int64  `json:"replayed_value_cents"`
	RecordedOnHand     int64  `json:"recorded_on_hand"`
	RecordedValueCents int64  `json:"recorded_value_cents"`
	HasPosition        bool   `json:"has_position"`
}

type ReplayReport struct {
	OK            bool             `json:"ok"`
	Tenant        string           `json:"tenant"`
	Positions     []ReplayPosition `json:"positions"`
	Issues        []string         `json:"issues"`
	MovementCount int64            `json:"movement_count"`
	CheckedAt     time.Time        `json:"checked_at"`
}

type replayKey struct {
	locationID string
	sku        string
}

type replayState struct {
	movementCount      int64
	replayedOnHand     int64
	replayedValueCents int64
	recordedOnHand     int64
	recordedValueCents int64
	hasPosition        bool
}

// Replay rebuilds position quantity and value from the append-only movement
// log and compares the result with the materialized positions. It never writes.
func (s *Service) Replay(ctx context.Context, tenant string) (ReplayReport, error) {
	if err := validateID("tenant", tenant); err != nil {
		return ReplayReport{}, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return ReplayReport{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	states := make(map[replayKey]*replayState)
	rows, err := tx.Query(ctx,
		`SELECT location_id, sku, COUNT(*), SUM(quantity_delta), SUM(value_delta_cents)
		 FROM inventory_movements
		 WHERE tenant_id = $1
		 GROUP BY location_id, sku`,
		tenant,
	)
	if err != nil {
		return ReplayReport{}, err
	}
	for rows.Next() {
		var key replayKey
		var state replayState
		if err := rows.Scan(&key.locationID, &key.sku, &state.movementCount, &state.replayedOnHand, &state.replayedValueCents); err != nil {
			rows.Close()
			return ReplayReport{}, err
		}
		states[key] = &state
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ReplayReport{}, err
	}

	rows, err = tx.Query(ctx,
		`SELECT location_id, sku, on_hand, inventory_value_cents
		 FROM positions
		 WHERE tenant_id = $1`,
		tenant,
	)
	if err != nil {
		return ReplayReport{}, err
	}
	for rows.Next() {
		var key replayKey
		var onHand, valueCents int64
		if err := rows.Scan(&key.locationID, &key.sku, &onHand, &valueCents); err != nil {
			rows.Close()
			return ReplayReport{}, err
		}
		state := states[key]
		if state == nil {
			state = &replayState{}
			states[key] = state
		}
		state.hasPosition = true
		state.recordedOnHand = onHand
		state.recordedValueCents = valueCents
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ReplayReport{}, err
	}

	keys := make([]replayKey, 0, len(states))
	for key := range states {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].locationID != keys[j].locationID {
			return keys[i].locationID < keys[j].locationID
		}
		return keys[i].sku < keys[j].sku
	})

	issues := make([]string, 0)
	positions := make([]ReplayPosition, 0, len(keys))
	var movementCount int64
	for _, key := range keys {
		state := states[key]
		positions = append(positions, ReplayPosition{
			LocationID:         key.locationID,
			SKU:                key.sku,
			MovementCount:      state.movementCount,
			ReplayedOnHand:     state.replayedOnHand,
			ReplayedValueCents: state.replayedValueCents,
			RecordedOnHand:     state.recordedOnHand,
			RecordedValueCents: state.recordedValueCents,
			HasPosition:        state.hasPosition,
		})
		movementCount += state.movementCount

		if !state.hasPosition {
			issues = append(issues, fmt.Sprintf("movements without position for %s/%s", key.locationID, key.sku))
			continue
		}
		if state.movementCount == 0 {
			issues = append(issues, fmt.Sprintf("position without movements for %s/%s", key.locationID, key.sku))
			continue
		}
		if state.replayedOnHand != state.recordedOnHand || state.replayedValueCents != state.recordedValueCents {
			issues = append(issues, fmt.Sprintf(
				"position drift for %s/%s: replayed (%d, %d) recorded (%d, %d)",
				key.locationID, key.sku,
				state.replayedOnHand, state.replayedValueCents,
				state.recordedOnHand, state.recordedValueCents,
			))
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ReplayReport{}, err
	}
	return ReplayReport{
		OK:            len(issues) == 0,
		Tenant:        tenant,
		Positions:     positions,
		Issues:        issues,
		MovementCount: movementCount,
		CheckedAt:     s.clock(),
	}, nil
}
