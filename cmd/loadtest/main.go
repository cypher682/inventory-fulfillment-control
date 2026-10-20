package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type config struct {
	base        string
	tenant      string
	location    string
	sku         string
	stock       int64
	attempts    int64
	workers     int
	idempotency bool
}

type position struct {
	OnHand    int64 `json:"on_hand"`
	Reserved  int64 `json:"reserved"`
	Available int64 `json:"available"`
}

type reconciliation struct {
	OK     bool     `json:"ok"`
	Issues []string `json:"issues"`
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.base, "base", "http://localhost:8092", "service base URL")
	flag.StringVar(&cfg.tenant, "tenant", "", "tenant id (default: generated)")
	flag.StringVar(&cfg.location, "location", "load-loc", "location id")
	flag.StringVar(&cfg.sku, "sku", "load-sku", "sku")
	flag.Int64Var(&cfg.stock, "stock", 100, "units to seed")
	flag.Int64Var(&cfg.attempts, "attempts", 300, "reservation attempts")
	flag.IntVar(&cfg.workers, "workers", 64, "concurrent workers")
	flag.BoolVar(&cfg.idempotency, "idempotency", false, "send a unique idempotency key per attempt")
	flag.Parse()

	if cfg.tenant == "" {
		cfg.tenant = fmt.Sprintf("load-%d", time.Now().UnixNano())
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "load test failed:", err)
		os.Exit(1)
	}
}

func run(cfg config) error {
	client := &http.Client{Timeout: 15 * time.Second}

	receipt := map[string]any{
		"location_id":     cfg.location,
		"sku":             cfg.sku,
		"quantity":        cfg.stock,
		"unit_cost_cents": 100,
	}
	status, body, err := do(client, cfg, http.MethodPost, "/v1/receipts", receipt, "seed")
	if err != nil {
		return err
	}
	if status != http.StatusCreated {
		return fmt.Errorf("seed receipt: status=%d body=%s", status, body)
	}

	var success, insufficient, failed atomic.Int64
	var next atomic.Int64
	start := time.Now()

	var wg sync.WaitGroup
	wg.Add(cfg.workers)
	for i := 0; i < cfg.workers; i++ {
		go func() {
			defer wg.Done()
			for {
				n := next.Add(1)
				if n > cfg.attempts {
					return
				}
				key := ""
				if cfg.idempotency {
					key = fmt.Sprintf("attempt-%d", n)
				}
				status, body, err := do(client, cfg, http.MethodPost, "/v1/reservations", map[string]any{
					"location_id": cfg.location,
					"sku":         cfg.sku,
					"quantity":    1,
				}, key)
				switch {
				case err != nil:
					failed.Add(1)
					fmt.Fprintf(os.Stderr, "attempt %d: %v\n", n, err)
				case status == http.StatusCreated:
					success.Add(1)
				case status == http.StatusConflict:
					insufficient.Add(1)
				default:
					failed.Add(1)
					fmt.Fprintf(os.Stderr, "attempt %d: status=%d body=%s\n", n, status, body)
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	var p position
	if err := get(client, cfg, "/v1/positions/"+cfg.location+"/"+cfg.sku, &p); err != nil {
		return err
	}
	var report reconciliation
	if err := get(client, cfg, "/v1/reconciliation", &report); err != nil {
		return err
	}

	rate := float64(cfg.attempts) / elapsed.Seconds()
	fmt.Printf("tenant=%s attempts=%d workers=%d\n", cfg.tenant, cfg.attempts, cfg.workers)
	fmt.Printf("success=%d rejected=%d errors=%d in %s (%.1f req/s)\n",
		success.Load(), insufficient.Load(), failed.Load(), elapsed.Round(time.Millisecond), rate)
	fmt.Printf("position: on_hand=%d reserved=%d available=%d\n", p.OnHand, p.Reserved, p.Available)
	fmt.Printf("reconciliation: ok=%v issues=%v\n", report.OK, report.Issues)

	if failed.Load() > 0 {
		return fmt.Errorf("%d requests failed", failed.Load())
	}
	if success.Load() != cfg.stock {
		return fmt.Errorf("sold %d reservations against %d units of stock", success.Load(), cfg.stock)
	}
	if p.Reserved != cfg.stock || p.Available != 0 {
		return fmt.Errorf("position invariant violated: %+v", p)
	}
	if !report.OK {
		return fmt.Errorf("reconciliation failed: %v", report.Issues)
	}
	return nil
}

func do(client *http.Client, cfg config, method, path string, payload any, idempotencyKey string) (int, string, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, "", err
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, cfg.base+path, body)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", cfg.tenant)
	req.Header.Set("X-Actor", "loadtest")
	if idempotencyKey != "" {
		req.Header.Set("X-Idempotency-Key", idempotencyKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(responseBody), nil
}

func get(client *http.Client, cfg config, path string, out any) error {
	status, body, err := do(client, cfg, http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET %s: status=%d body=%s", path, status, body)
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
		return fmt.Errorf("decode GET %s: %w", path, err)
	}
	return nil
}
