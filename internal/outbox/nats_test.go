package outbox

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

func TestNATSPublisherPersistsAndDeduplicates(t *testing.T) {
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL is not set; start deploy/docker-compose.yml and set it to run the NATS test")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	suffix := time.Now().UnixNano()
	stream := fmt.Sprintf("test_inventory_%d", suffix)
	prefix := fmt.Sprintf("test.%d.", suffix)

	publisher, err := NewNATSPublisher(ctx, url, stream, prefix)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	defer publisher.Close()
	defer func() { _ = publisher.js.DeleteStream(context.Background(), stream) }()

	message := Message{
		ID:       7,
		TenantID: "demo",
		Topic:    "inventory.events",
		Payload:  []byte(`{"event_type":"inventory.received"}`),
	}
	if err := publisher.Publish(ctx, message); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if err := publisher.Publish(ctx, message); err != nil {
		t.Fatalf("duplicate publish: %v", err)
	}

	consumer, err := publisher.js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:       "verify",
		FilterSubject: prefix + "inventory.events",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	batch, err := consumer.Fetch(2, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	count := 0
	for msg := range batch.Messages() {
		count++
		if string(msg.Data()) != string(message.Payload) {
			t.Fatalf("payload = %s", msg.Data())
		}
		if err := msg.Ack(); err != nil {
			t.Fatalf("ack: %v", err)
		}
	}
	if count != 1 {
		t.Fatalf("delivered %d messages, want 1 after dedupe", count)
	}
}
