package outbox

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type NATSPublisher struct {
	conn   *nats.Conn
	js     jetstream.JetStream
	prefix string
}

// NewNATSPublisher connects to NATS, enables a durable JetStream stream, and
// returns a publisher that waits for a broker acknowledgement on every message.
func NewNATSPublisher(ctx context.Context, url, stream, prefix string) (*NATSPublisher, error) {
	if url == "" {
		return nil, fmt.Errorf("NATS URL is required")
	}
	if stream == "" || strings.ContainsAny(stream, ".*>/\\ \t\n") {
		return nil, fmt.Errorf("invalid NATS stream %q", stream)
	}
	if prefix != "" && (!strings.HasSuffix(prefix, ".") || strings.ContainsAny(prefix, "*> \t\n")) {
		return nil, fmt.Errorf("NATS subject prefix %q must end with '.' and contain no wildcards or spaces", prefix)
	}

	conn, err := nats.Connect(url,
		nats.Name("inventory-outbox"),
		nats.Timeout(5*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		return nil, fmt.Errorf("connect NATS: %w", err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("create JetStream context: %w", err)
	}

	filter := "inventory.>"
	if prefix != "" {
		filter = prefix + ">"
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       stream,
		Subjects:   []string{filter},
		Retention:  jetstream.LimitsPolicy,
		Storage:    jetstream.FileStorage,
		Discard:    jetstream.DiscardOld,
		MaxAge:     7 * 24 * time.Hour,
		Duplicates: time.Hour,
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ensure NATS stream %q: %w", stream, err)
	}

	return &NATSPublisher{conn: conn, js: js, prefix: prefix}, nil
}

func (p *NATSPublisher) Publish(ctx context.Context, m Message) error {
	if m.Topic == "" || strings.ContainsAny(m.Topic, "*> \t\n") {
		return fmt.Errorf("invalid NATS topic %q", m.Topic)
	}
	subject := m.Topic
	if p.prefix != "" {
		subject = p.prefix + m.Topic
	}
	if _, err := p.js.Publish(ctx, subject, m.Payload, jetstream.WithMsgID(strconv.FormatInt(m.ID, 10))); err != nil {
		return fmt.Errorf("publish NATS subject %q: %w", subject, err)
	}
	return nil
}

func (p *NATSPublisher) Close() error {
	p.conn.Close()
	return nil
}
