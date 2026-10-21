package inventory

import (
	"context"
	"fmt"
)

const (
	defaultExpiryBatch = 100
	maxExpiryBatch     = 1000
)

// ExpireReservations releases reservations whose expiry has passed. Rows are
// claimed with SKIP LOCKED so multiple service instances can sweep safely.
// The reserved quantity is restored in the same transaction that marks the
// reservation expired, keeping positions and active reservations consistent.
func (s *Service) ExpireReservations(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = defaultExpiryBatch
	}
	if limit > maxExpiryBatch {
		limit = maxExpiryBatch
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	type candidate struct {
		tenantID   string
		id         string
		locationID string
		sku        string
		quantity   int64
	}

	rows, err := tx.Query(ctx,
		`SELECT tenant_id, id, location_id, sku, quantity
		 FROM reservations
		 WHERE status = 'active' AND expires_at <= $1
		 ORDER BY expires_at, id
		 LIMIT $2
		 FOR UPDATE SKIP LOCKED`,
		s.clock(), limit,
	)
	if err != nil {
		return 0, err
	}

	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.tenantID, &c.id, &c.locationID, &c.sku, &c.quantity); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, c := range candidates {
		tag, err := tx.Exec(ctx,
			`UPDATE positions
			 SET reserved = reserved - $4, version = version + 1, updated_at = now()
			 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3 AND reserved >= $4`,
			c.tenantID, c.locationID, c.sku, c.quantity,
		)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() != 1 {
			return 0, fmt.Errorf("expire reservation %s: position %s/%s does not hold %d reserved",
				c.id, c.locationID, c.sku, c.quantity)
		}

		tag, err = tx.Exec(ctx,
			`UPDATE reservations
			 SET status = 'expired', updated_at = now()
			 WHERE tenant_id = $1 AND id = $2 AND status = 'active'`,
			c.tenantID, c.id,
		)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() != 1 {
			return 0, fmt.Errorf("expire reservation %s: state changed during sweep", c.id)
		}

		if err := s.emit(ctx, tx, c.tenantID, "reservation", c.id, "reservation.expired", "expiry-sweeper", map[string]any{
			"reservation_id": c.id,
			"location_id":    c.locationID,
			"sku":            c.sku,
			"quantity":       c.quantity,
		}); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if len(candidates) > 0 {
		s.metrics.expirations.Add(int64(len(candidates)))
	}
	return len(candidates), nil
}
