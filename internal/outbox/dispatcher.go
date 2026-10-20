package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Run(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := drain(ctx, pool, log); err != nil {
				log.Error("outbox drain failed", "error", err)
			}
		}
	}
}

func drain(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	rows, err := pool.Query(ctx, `
		WITH claimed AS (
			SELECT id
			FROM outbox
			WHERE published_at IS NULL
			ORDER BY id
			LIMIT 100
			FOR UPDATE SKIP LOCKED
		)
		UPDATE outbox o
		SET published_at = now()
		FROM claimed c
		WHERE o.id = c.id
		RETURNING o.id, o.tenant_id, o.topic, o.payload`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var tenant, topic string
		var payload []byte
		if err := rows.Scan(&id, &tenant, &topic, &payload); err != nil {
			return err
		}
		log.Info("outbox event", "id", id, "tenant", tenant, "topic", topic, "bytes", len(payload))
	}
	return rows.Err()
}
