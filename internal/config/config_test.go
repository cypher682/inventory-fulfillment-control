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
