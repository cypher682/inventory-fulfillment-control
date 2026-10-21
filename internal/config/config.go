package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	ServiceName    string
	Port           int
	LogLevel       string
	DatabaseURL    string
	ExpiryInterval time.Duration
	ExpiryBatch    int
	OutboxInterval time.Duration
	OutboxBatch    int
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

	return cfg, nil
}

func (c Config) Addr() string {
	return fmt.Sprintf(":%d", c.Port)
}
