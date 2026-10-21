package inventory

import "context"

func (s *Service) OutboxPending(ctx context.Context) (count int64, oldestSeconds float64, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(EXTRACT(EPOCH FROM now() - MIN(created_at)), 0)
		 FROM outbox
		 WHERE published_at IS NULL`,
	).Scan(&count, &oldestSeconds)
	return count, oldestSeconds, err
}
