package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultBatch      = 100
	defaultLease      = 30 * time.Second
	defaultMaxBackoff = 300
)

type Message struct {
	ID       int64
	TenantID string
	Topic    string
	Payload  []byte
	Attempts int
}

type Publisher interface {
	Publish(context.Context, Message) error
}

type PublisherFunc func(context.Context, Message) error

func (f PublisherFunc) Publish(ctx context.Context, m Message) error {
	return f(ctx, m)
}

type LogPublisher struct {
	Log *slog.Logger
}

func (p LogPublisher) Publish(_ context.Context, m Message) error {
	var envelope struct {
		EventType string `json:"event_type"`
	}
	_ = json.Unmarshal(m.Payload, &envelope)
	p.Log.Info("outbox event",
		"id", m.ID,
		"tenant", m.TenantID,
		"topic", m.Topic,
		"event_type", envelope.EventType,
		"attempt", m.Attempts,
		"bytes", len(m.Payload),
	)
	return nil
}

type Relay struct {
	pool       *pgxpool.Pool
	publisher  Publisher
	log        *slog.Logger
	batch      int
	interval   time.Duration
	lease      time.Duration
	maxBackoff int
}

func NewRelay(pool *pgxpool.Pool, publisher Publisher, log *slog.Logger) *Relay {
	return &Relay{
		pool:       pool,
		publisher:  publisher,
		log:        log,
		batch:      defaultBatch,
		interval:   time.Second,
		lease:      defaultLease,
		maxBackoff: defaultMaxBackoff,
	}
}

func (r *Relay) WithInterval(interval time.Duration) *Relay {
	if interval > 0 {
		r.interval = interval
	}
	return r
}

func (r *Relay) WithBatch(batch int) *Relay {
	if batch > 0 {
		r.batch = batch
	}
	return r
}

func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.Drain(ctx); err != nil && !errors.Is(err, context.Canceled) {
				r.log.Error("outbox drain failed", "error", err)
			}
		}
	}
}

// Drain claims a batch under a lease, publishes it, and marks successful
// messages. A crash after claiming leaves rows unpublished; they become
// eligible again when the lease expires. Delivery is therefore at-least-once.
func (r *Relay) Drain(ctx context.Context) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		WITH claimed AS (
			SELECT id
			FROM outbox
			WHERE published_at IS NULL AND next_attempt_at <= now()
			ORDER BY id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE outbox o
		SET attempts = o.attempts + 1,
			next_attempt_at = now() + make_interval(secs => $2)
		FROM claimed c
		WHERE o.id = c.id
		RETURNING o.id, o.tenant_id, o.topic, o.payload, o.attempts`,
		r.batch, int(r.lease.Seconds()),
	)
	if err != nil {
		return 0, err
	}

	var messages []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.TenantID, &m.Topic, &m.Payload, &m.Attempts); err != nil {
			rows.Close()
			return 0, err
		}
		messages = append(messages, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if len(messages) == 0 {
		return 0, nil
	}

	published := 0
	for _, m := range messages {
		if err := r.publisher.Publish(ctx, m); err != nil {
			if uerr := r.markFailed(ctx, m, err); uerr != nil {
				return published, fmt.Errorf("publish outbox %d: %w (record failure: %v)", m.ID, err, uerr)
			}
			return published, fmt.Errorf("publish outbox %d: %w", m.ID, err)
		}
		if err := r.markPublished(ctx, m.ID); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}

func (r *Relay) markPublished(ctx context.Context, id int64) error {
	if _, err := r.pool.Exec(ctx,
		`UPDATE outbox SET published_at = now(), last_error = NULL WHERE id = $1 AND published_at IS NULL`,
		id,
	); err != nil {
		return fmt.Errorf("mark outbox %d published: %w", id, err)
	}
	return nil
}

func (r *Relay) markFailed(ctx context.Context, m Message, publishErr error) error {
	if _, err := r.pool.Exec(ctx,
		`UPDATE outbox
		 SET last_error = $2,
		     next_attempt_at = now() + make_interval(secs => LEAST(1 << LEAST(attempts, 16), $3))
		 WHERE id = $1 AND published_at IS NULL`,
		m.ID, publishErr.Error(), r.maxBackoff,
	); err != nil {
		return fmt.Errorf("mark outbox %d failed: %w", m.ID, err)
	}
	return nil
}
