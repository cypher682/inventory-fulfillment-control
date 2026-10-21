package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type config struct {
	base        string
	tenant      string
	location    string
	sku         string
	skus        int
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
	flag.StringVar(&cfg.sku, "sku", "load-sku", "sku prefix")
	flag.IntVar(&cfg.skus, "skus", 1, "number of SKUs to spread attempts across")
	flag.Int64Var(&cfg.stock, "stock", 100, "total units to seed across all SKUs")
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
	if cfg.skus < 1 {
		return fmt.Errorf("skus must be >= 1")
	}
	if cfg.stock < int64(cfg.skus) {
		return fmt.Errorf("stock (%d) must be >= skus (%d)", cfg.stock, cfg.skus)
	}
	if cfg.attempts < cfg.stock {
		return fmt.Errorf("attempts (%d) must be >= stock (%d)", cfg.attempts, cfg.stock)
	}
	client := &http.Client{Timeout: 15 * time.Second}

	skus := make([]string, cfg.skus)
	seeded := make([]int64, cfg.skus)
	perSKU := cfg.stock / int64(cfg.skus)
	remainder := cfg.stock % int64(cfg.skus)
	for i := range skus {
		skus[i] = cfg.sku
		if cfg.skus > 1 {
			skus[i] = fmt.Sprintf("%s-%02d", cfg.sku, i)
		}
		seeded[i] = perSKU
		if int64(i) < remainder {
			seeded[i]++
		}
		receipt := map[string]any{
			"location_id":     cfg.location,
			"sku":             skus[i],
			"quantity":        seeded[i],
			"unit_cost_cents": 100,
		}
		status, body, err := do(client, cfg, http.MethodPost, "/v1/receipts", receipt, "seed-"+skus[i])
		if err != nil {
			return err
		}
		if status != http.StatusCreated {
			return fmt.Errorf("seed receipt for %s: status=%d body=%s", skus[i], status, body)
		}
	}

	var success, insufficient, failed atomic.Int64
	var next atomic.Int64
	durations := make([]time.Duration, cfg.attempts)
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
				sku := skus[(n-1)%int64(len(skus))]
				requestStart := time.Now()
				status, body, err := do(client, cfg, http.MethodPost, "/v1/reservations", map[string]any{
					"location_id": cfg.location,
					"sku":         sku,
					"quantity":    1,
				}, key)
				durations[n-1] = time.Since(requestStart)
				switch {
				case err != nil:
					failed.Add(1)
					fmt.Fprintf(os.Stderr, "attempt %d (%s): %v\n", n, sku, err)
				case status == http.StatusCreated:
					success.Add(1)
				case status == http.StatusConflict:
					insufficient.Add(1)
				default:
					failed.Add(1)
					fmt.Fprintf(os.Stderr, "attempt %d (%s): status=%d body=%s\n", n, sku, status, body)
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	var report reconciliation
	if err := get(client, cfg, "/v1/reconciliation", &report); err != nil {
		return err
	}

	rate := float64(cfg.attempts) / elapsed.Seconds()
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	fmt.Printf("tenant=%s attempts=%d workers=%d skus=%d stock=%d\n", cfg.tenant, cfg.attempts, cfg.workers, cfg.skus, cfg.stock)
	fmt.Printf("success=%d rejected=%d errors=%d in %s (%.1f req/s)\n",
		success.Load(), insufficient.Load(), failed.Load(), elapsed.Round(time.Millisecond), rate)
	fmt.Printf("latency: p50=%s p95=%s p99=%s max=%s\n",
		percentile(durations, 0.50), percentile(durations, 0.95), percentile(durations, 0.99), percentile(durations, 1))
	fmt.Printf("reconciliation: ok=%v issues=%v\n", report.OK, report.Issues)

	if failed.Load() > 0 {
		return fmt.Errorf("%d requests failed", failed.Load())
	}
	if success.Load() != cfg.stock {
		return fmt.Errorf("sold %d reservations against %d units of stock", success.Load(), cfg.stock)
	}
	for i, sku := range skus {
		var p position
		if err := get(client, cfg, "/v1/positions/"+cfg.location+"/"+sku, &p); err != nil {
			return err
		}
		if p.OnHand != seeded[i] || p.Reserved != seeded[i] || p.Available != 0 {
			return fmt.Errorf("position %s invariant violated: on_hand=%d reserved=%d available=%d want on_hand=reserved=%d",
				sku, p.OnHand, p.Reserved, p.Available, seeded[i])
		}
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

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	return sorted[int(float64(len(sorted)-1)*p)]
}
