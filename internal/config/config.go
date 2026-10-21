package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const minAPIKeyLength = 16

type Config struct {
	ServiceName       string
	Port              int
	LogLevel          string
	DatabaseURL       string
	ExpiryInterval    time.Duration
	ExpiryBatch       int
	OutboxInterval    time.Duration
	OutboxBatch       int
	OutboxPublisher   string
	NATSURL           string
	NATSStream        string
	NATSSubjectPrefix string
	WebhookURL        string
	WebhookSecret     string
	AuthRequired      bool
	APIKeys           map[string]string
}

func Load() (Config, error) {
	cfg := Config{
		ServiceName:     "inventory",
		Port:            8092,
		LogLevel:        "info",
		DatabaseURL:     "postgres://inventory:inventory@localhost:5442/inventory?sslmode=disable",
		ExpiryInterval:  10 * time.Second,
		ExpiryBatch:     100,
		OutboxInterval:  time.Second,
		OutboxBatch:     100,
		OutboxPublisher: "log",
		NATSURL:         "nats://localhost:4222",
		NATSStream:      "inventory",
		APIKeys:         make(map[string]string),
	}

	if v := os.Getenv("SERVICE_NAME"); v != "" {
		cfg.ServiceName = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		cfg.DatabaseURL = v
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
	if v := os.Getenv("OUTBOX_PUBLISHER"); v != "" {
		cfg.OutboxPublisher = strings.ToLower(strings.TrimSpace(v))
	}
	if v := os.Getenv("NATS_URL"); v != "" {
		cfg.NATSURL = strings.TrimSpace(v)
	}
	if v := os.Getenv("NATS_STREAM"); v != "" {
		cfg.NATSStream = strings.TrimSpace(v)
	}
	if v := os.Getenv("NATS_SUBJECT_PREFIX"); v != "" {
		cfg.NATSSubjectPrefix = strings.TrimSpace(v)
	}
	if v := os.Getenv("WEBHOOK_URL"); v != "" {
		cfg.WebhookURL = strings.TrimSpace(v)
	}
	if v := os.Getenv("WEBHOOK_SECRET"); v != "" {
		cfg.WebhookSecret = v
	}
	if err := validatePublisher(cfg); err != nil {
		return Config{}, err
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

func validatePublisher(cfg Config) error {
	switch cfg.OutboxPublisher {
	case "log":
		return nil
	case "nats":
		if cfg.NATSURL == "" {
			return fmt.Errorf("OUTBOX_PUBLISHER=nats requires NATS_URL")
		}
		if cfg.NATSStream == "" || strings.ContainsAny(cfg.NATSStream, ".*>/\\ \t\n") {
			return fmt.Errorf("invalid NATS_STREAM %q", cfg.NATSStream)
		}
		if prefix := cfg.NATSSubjectPrefix; prefix != "" {
			if !strings.HasSuffix(prefix, ".") || strings.ContainsAny(prefix, "*> \t\n") {
				return fmt.Errorf("NATS_SUBJECT_PREFIX %q must end with '.' and contain no wildcards or spaces", prefix)
			}
		}
		return nil
	case "webhook":
		parsed, err := url.Parse(cfg.WebhookURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("OUTBOX_PUBLISHER=webhook requires a valid HTTP(S) WEBHOOK_URL")
		}
		if len(cfg.WebhookSecret) < minAPIKeyLength {
			return fmt.Errorf("WEBHOOK_SECRET must be at least %d characters", minAPIKeyLength)
		}
		return nil
	default:
		return fmt.Errorf("invalid OUTBOX_PUBLISHER %q: must be log, nats, or webhook", cfg.OutboxPublisher)
	}
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
