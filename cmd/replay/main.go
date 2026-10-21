package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cypher682/inventory-fulfillment-control/internal/config"
	"github.com/cypher682/inventory-fulfillment-control/internal/inventory"
	"github.com/cypher682/inventory-fulfillment-control/internal/store"
)

func main() {
	tenant := flag.String("tenant", "", "tenant to replay")
	databaseURL := flag.String("database-url", "", "database URL (defaults to DATABASE_URL or the local compose DSN)")
	flag.Parse()

	if *tenant == "" {
		fmt.Fprintln(os.Stderr, "-tenant is required")
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	dsn := cfg.DatabaseURL
	if *databaseURL != "" {
		dsn = *databaseURL
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := store.Open(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	report, err := inventory.NewService(pool).Replay(ctx, *tenant)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay: %v\n", err)
		os.Exit(1)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode report: %v\n", err)
		os.Exit(1)
	}
	if !report.OK {
		os.Exit(1)
	}
}
