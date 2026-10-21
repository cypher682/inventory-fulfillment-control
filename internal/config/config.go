package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const minAPIKeyLength = 16

type Config struct {
	ServiceName    string
	Port           int
	LogLevel       string
	DatabaseURL    string
	ExpiryInterval time.Duration
	ExpiryBatch    int
	OutboxInterval time.Duration
	OutboxBatch    int
	AuthRequired   bool
	APIKeys        map[string]string
}

func Load() (Config, error) {
	cfg := Config{
		ServiceName:    "inventory",
		Port:           8092,
		LogLevel:       "info",
		DatabaseURL:    "postgres://inventory:inventory@localhost:5442/inventory?sslmode=disable",
		ExpiryInterval: 10 * time.Second,
		ExpiryBatch:    100,
		OutboxInterval: time.Second,
		OutboxBatch:    100,
		APIKeys:        make(map[string]string),
	}

	if v := os.Getenv("SERVICE_NAME"); v != "" {
		cfg.ServiceName = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			return Config{}, fmt.Errorf("invalid PORT %q", v)
		}
		cfg.Port = port
	}
	if v := os.Getenv("EXPIRY_INTERVAL"); v != "" {
		interval, err := time.ParseDuration(v)
		if err != nil || interval < time.Second {
			return Config{}, fmt.Errorf("invalid EXPIRY_INTERVAL %q: must be a duration of at least 1s", v)
		}
		cfg.ExpiryInterval = interval
	}
	if v := os.Getenv("EXPIRY_BATCH"); v != "" {
		batch, err := strconv.Atoi(v)
		if err != nil || batch < 1 || batch > 1000 {
			return Config{}, fmt.Errorf("invalid EXPIRY_BATCH %q: must be between 1 and 1000", v)
		}
		cfg.ExpiryBatch = batch
	}
	if v := os.Getenv("OUTBOX_INTERVAL"); v != "" {
		interval, err := time.ParseDuration(v)
		if err != nil || interval < 100*time.Millisecond {
			return Config{}, fmt.Errorf("invalid OUTBOX_INTERVAL %q: must be a duration of at least 100ms", v)
		}
		cfg.OutboxInterval = interval
	}
	if v := os.Getenv("OUTBOX_BATCH"); v != "" {
		batch, err := strconv.Atoi(v)
		if err != nil || batch < 1 || batch > 1000 {
			return Config{}, fmt.Errorf("invalid OUTBOX_BATCH %q: must be between 1 and 1000", v)
		}
		cfg.OutboxBatch = batch
	}
	if v := os.Getenv("AUTH_REQUIRED"); v != "" {
		required, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid AUTH_REQUIRED %q", v)
		}
		cfg.AuthRequired = required
	}
	if v := os.Getenv("API_KEYS"); v != "" {
		keys, err := parseAPIKeys(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid API_KEYS: %w", err)
		}
		cfg.APIKeys = keys
	}
	if cfg.AuthRequired && len(cfg.APIKeys) == 0 {
		return Config{}, fmt.Errorf("AUTH_REQUIRED is true but API_KEYS is empty")
	}

	return cfg, nil
}

func parseAPIKeys(raw string) (map[string]string, error) {
	keys := make(map[string]string)
	seen := make(map[string]string)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		tenant, key, ok := strings.Cut(entry, ":")
		tenant = strings.TrimSpace(tenant)
		key = strings.TrimSpace(key)
		if !ok || tenant == "" || key == "" {
			return nil, fmt.Errorf("entry %q must be tenant:key", entry)
		}
		if len(tenant) > 64 {
			return nil, fmt.Errorf("tenant %q is longer than 64 characters", tenant)
		}
		if len(key) < minAPIKeyLength {
			return nil, fmt.Errorf("key for tenant %q is shorter than %d characters", tenant, minAPIKeyLength)
		}
		if _, exists := keys[tenant]; exists {
			return nil, fmt.Errorf("duplicate tenant %q", tenant)
		}
		if other, exists := seen[key]; exists {
			return nil, fmt.Errorf("key for tenant %q is already used by tenant %q", tenant, other)
		}
		keys[tenant] = key
		seen[key] = tenant
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no keys configured")
	}
	return keys, nil
}

func (c Config) Addr() string {
	return fmt.Sprintf(":%d", c.Port)
}
