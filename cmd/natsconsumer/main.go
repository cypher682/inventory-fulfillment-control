package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	natsURL := flag.String("nats-url", envOr("NATS_URL", "nats://localhost:4222"), "NATS URL")
	stream := flag.String("stream", envOr("NATS_STREAM", "inventory"), "JetStream stream")
	filter := flag.String("filter", envOr("NATS_SUBJECT_PREFIX", "")+"inventory.>", "subject filter")
	durable := flag.String("durable", "inventory-printer", "durable consumer name")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn, err := nats.Connect(*natsURL, nats.Name("inventory-consumer"), nats.Timeout(5*time.Second))
	if err != nil {
		log.Error("connect NATS", "error", err)
		os.Exit(1)
	}
	defer conn.Close()

	js, err := jetstream.New(conn)
	if err != nil {
		log.Error("create JetStream context", "error", err)
		os.Exit(1)
	}
	var consumer jetstream.Consumer
	deadline := time.Now().Add(30 * time.Second)
	for {
		consumer, err = js.CreateOrUpdateConsumer(ctx, *stream, jetstream.ConsumerConfig{
			Durable:       *durable,
			FilterSubject: *filter,
			AckPolicy:     jetstream.AckExplicitPolicy,
			DeliverPolicy: jetstream.DeliverAllPolicy,
		})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			log.Error("create consumer", "error", err)
			os.Exit(1)
		}
		log.Warn("consumer not ready; retrying", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}

	consume, err := consumer.Consume(func(msg jetstream.Msg) {
		log.Info("event",
			"subject", msg.Subject(),
			"bytes", len(msg.Data()),
			"data", string(msg.Data()),
		)
		if err := msg.Ack(); err != nil {
			log.Error("ack", "error", err)
		}
	})
	if err != nil {
		log.Error("consume", "error", err)
		os.Exit(1)
	}
	defer consume.Stop()

	log.Info("consuming", "stream", *stream, "filter", *filter, "durable", *durable)
	<-ctx.Done()
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
