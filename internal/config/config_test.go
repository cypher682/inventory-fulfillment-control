package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("SERVICE_NAME", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("EXPIRY_INTERVAL", "")
	t.Setenv("EXPIRY_BATCH", "")
	t.Setenv("OUTBOX_INTERVAL", "")
	t.Setenv("OUTBOX_BATCH", "")
	t.Setenv("AUTH_REQUIRED", "")
	t.Setenv("API_KEYS", "")
	t.Setenv("OUTBOX_PUBLISHER", "")
	t.Setenv("NATS_URL", "")
	t.Setenv("NATS_STREAM", "")
	t.Setenv("NATS_SUBJECT_PREFIX", "")
	t.Setenv("WEBHOOK_URL", "")
	t.Setenv("WEBHOOK_SECRET", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != 8092 {
		t.Fatalf("Port = %d, want 8092", cfg.Port)
	}
	if cfg.ServiceName != "inventory" {
		t.Fatalf("ServiceName = %q, want inventory", cfg.ServiceName)
	}
	if cfg.ExpiryInterval != 10*time.Second {
		t.Fatalf("ExpiryInterval = %s, want 10s", cfg.ExpiryInterval)
	}
	if cfg.ExpiryBatch != 100 || cfg.OutboxBatch != 100 {
		t.Fatalf("batches = %d/%d, want 100/100", cfg.ExpiryBatch, cfg.OutboxBatch)
	}
	if cfg.OutboxInterval != time.Second {
		t.Fatalf("OutboxInterval = %s, want 1s", cfg.OutboxInterval)
	}
	if cfg.AuthRequired || len(cfg.APIKeys) != 0 {
		t.Fatalf("auth defaults = %v/%v, want disabled and empty", cfg.AuthRequired, cfg.APIKeys)
	}
	if cfg.OutboxPublisher != "log" || cfg.NATSURL != "nats://localhost:4222" || cfg.NATSStream != "inventory" {
		t.Fatalf("publisher defaults = %q %q %q", cfg.OutboxPublisher, cfg.NATSURL, cfg.NATSStream)
	}
}

func TestLoadPublisherConfig(t *testing.T) {
	t.Setenv("OUTBOX_PUBLISHER", "NATS")
	t.Setenv("NATS_URL", "nats://nats:4222")
	t.Setenv("NATS_STREAM", "inventory-prod")
	t.Setenv("NATS_SUBJECT_PREFIX", "prod.")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() nats error = %v", err)
	}
	if cfg.OutboxPublisher != "nats" || cfg.NATSSubjectPrefix != "prod." {
		t.Fatalf("nats config = %+v", cfg)
	}

	t.Setenv("OUTBOX_PUBLISHER", "webhook")
	t.Setenv("WEBHOOK_URL", "https://events.example.com/inventory")
	t.Setenv("WEBHOOK_SECRET", "0123456789abcdef")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() webhook error = %v", err)
	}
}

func TestLoadRejectsInvalidPublisher(t *testing.T) {
	for _, tc := range []struct {
		publisher, url, secret, stream, prefix string
	}{
		{publisher: "kafka"},
		{publisher: "nats", url: "nats://localhost:4222", stream: "bad.stream"},
		{publisher: "nats", url: "nats://localhost:4222", stream: "inventory", prefix: "prod"},
		{publisher: "webhook", url: "", secret: "0123456789abcdef"},
		{publisher: "webhook", url: "ftp://example.com", secret: "0123456789abcdef"},
		{publisher: "webhook", url: "https://example.com", secret: "short"},
	} {
		t.Setenv("OUTBOX_PUBLISHER", tc.publisher)
		t.Setenv("NATS_URL", tc.url)
		t.Setenv("NATS_STREAM", tc.stream)
		t.Setenv("NATS_SUBJECT_PREFIX", tc.prefix)
		t.Setenv("WEBHOOK_URL", tc.url)
		t.Setenv("WEBHOOK_SECRET", tc.secret)
		if _, err := Load(); err == nil {
			t.Fatalf("Load() publisher=%q url=%q = nil error", tc.publisher, tc.url)
		}
	}
}

func TestLoadAPIKeys(t *testing.T) {
	t.Setenv("AUTH_REQUIRED", "true")
	t.Setenv("API_KEYS", "demo:0123456789abcdef,acme:fedcba9876543210")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.AuthRequired || len(cfg.APIKeys) != 2 {
		t.Fatalf("auth = %v/%v", cfg.AuthRequired, cfg.APIKeys)
	}
	if cfg.APIKeys["demo"] != "0123456789abcdef" || cfg.APIKeys["acme"] != "fedcba9876543210" {
		t.Fatalf("keys = %v", cfg.APIKeys)
	}
}

func TestLoadRejectsInvalidAPIKeys(t *testing.T) {
	t.Setenv("AUTH_REQUIRED", "true")
	for _, raw := range []string{
		"demo",
		"demo:short",
		"demo:0123456789abcdef,demo:fedcba9876543210",
		"demo:0123456789abcdef,acme:0123456789abcdef",
		"0123456789abcdef",
	} {
		t.Setenv("API_KEYS", raw)
		if _, err := Load(); err == nil {
			t.Fatalf("Load() with API_KEYS %q = nil error", raw)
		}
	}

	t.Setenv("AUTH_REQUIRED", "false")
	t.Setenv("API_KEYS", "")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() without auth error = %v", err)
	}

	t.Setenv("AUTH_REQUIRED", "true")
	if _, err := Load(); err == nil {
		t.Fatal("AUTH_REQUIRED without keys = nil error")
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	t.Setenv("PORT", "abc")

	if _, err := Load(); err == nil {
		t.Fatal("Load() = nil error, want invalid PORT error")
	}
}

func TestLoadRejectsInvalidExpiry(t *testing.T) {
	t.Setenv("EXPIRY_INTERVAL", "500ms")
	if _, err := Load(); err == nil {
		t.Fatal("Load() = nil error, want invalid EXPIRY_INTERVAL error")
	}

	t.Setenv("EXPIRY_INTERVAL", "10s")
	t.Setenv("EXPIRY_BATCH", "0")
	if _, err := Load(); err == nil {
		t.Fatal("Load() = nil error, want invalid EXPIRY_BATCH error")
	}
}
