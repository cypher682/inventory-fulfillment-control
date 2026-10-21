package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cypher682/inventory-fulfillment-control/internal/config"
	"github.com/cypher682/inventory-fulfillment-control/internal/health"
	"github.com/cypher682/inventory-fulfillment-control/internal/inventory"
	"github.com/cypher682/inventory-fulfillment-control/internal/outbox"
	"github.com/cypher682/inventory-fulfillment-control/internal/store"
)

var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("connect database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := store.Migrate(ctx, pool); err != nil {
		log.Error("migrate database", "error", err)
		os.Exit(1)
	}

	service := inventory.NewService(pool)
	api := inventory.NewServer(service, version, log)

	h := health.New(cfg.ServiceName, version)
	h.AddCheck("database", func(ctx context.Context) error { return pool.Ping(ctx) })

	mux := http.NewServeMux()
	api.Register(mux)
	mux.HandleFunc("GET /healthz", h.Liveness)
	mux.HandleFunc("GET /readyz", h.Readiness)

	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           api.Handler(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	relay := outbox.NewRelay(pool, outbox.LogPublisher{Log: log}, log).
		WithInterval(cfg.OutboxInterval).
		WithBatch(cfg.OutboxBatch)
	go relay.Run(ctx)

	go runExpiry(ctx, service, log, cfg.ExpiryInterval, cfg.ExpiryBatch)

	go func() {
		log.Info("server started", "addr", cfg.Addr(), "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "error", err)
		os.Exit(1)
	}
	log.Info("server stopped")
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func runExpiry(ctx context.Context, service *inventory.Service, log *slog.Logger, interval time.Duration, batch int) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expired, err := service.ExpireReservations(ctx, batch)
			if err != nil {
				log.Error("expire reservations", "error", err)
				continue
			}
			if expired > 0 {
				log.Info("expired reservations", "count", expired)
			}
		}
	}
}
