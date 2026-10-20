package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	ServiceName string
	Port        int
	LogLevel    string
	DatabaseURL string
}

func Load() (Config, error) {
	cfg := Config{
		ServiceName: "inventory",
		Port:        8082,
		LogLevel:    "info",
		DatabaseURL: os.Getenv("DATABASE_URL"),
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

	return cfg, nil
}

func (c Config) Addr() string {
	return fmt.Sprintf(":%d", c.Port)
}
