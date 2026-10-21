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
