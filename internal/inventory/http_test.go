package inventory

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testHTTP(t *testing.T) (*httptest.Server, *Service) {
	t.Helper()
	svc, _ := testService(t)
	api := NewServer(svc, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	api.Register(mux)
	server := httptest.NewServer(api.Handler(mux))
	t.Cleanup(server.Close)
	return server, svc
}

func doRequest(t *testing.T, client *http.Client, method, url, tenant, key, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if tenant != "" {
		req.Header.Set("X-Tenant-ID", tenant)
	}
	if key != "" {
		req.Header.Set("X-Idempotency-Key", key)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decodeBody[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var value T
	if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return value
}

func TestHTTPFlow(t *testing.T) {
	server, _ := testHTTP(t)
	client := server.Client()
	tenant := "tenant-http"

	resp := doRequest(t, client, http.MethodPost, server.URL+"/v1/receipts", tenant, "receipt-1",
		`{"location_id":"loc-http","sku":"sku-http","quantity":5,"unit_cost_cents":100}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("receipt status = %d, body = %s", resp.StatusCode, readAll(t, resp))
	}
	position := decodeBody[Position](t, resp)
	if position.OnHand != 5 || position.Available != 5 {
		t.Fatalf("position = %+v", position)
	}

	resp = doRequest(t, client, http.MethodPost, server.URL+"/v1/receipts", tenant, "receipt-1",
		`{"location_id":"loc-http","sku":"sku-http","quantity":5,"unit_cost_cents":100}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("replay status = %d, body = %s", resp.StatusCode, readAll(t, resp))
	}
	if resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("missing Idempotent-Replayed header")
	}

	resp = doRequest(t, client, http.MethodPost, server.URL+"/v1/reservations", tenant, "reserve-1",
		`{"location_id":"loc-http","sku":"sku-http","quantity":2}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("reserve status = %d, body = %s", resp.StatusCode, readAll(t, resp))
	}
	reservation := decodeBody[Reservation](t, resp)

	resp = doRequest(t, client, http.MethodPost, server.URL+"/v1/reservations/"+reservation.ID+"/confirm", tenant, "confirm-1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm status = %d, body = %s", resp.StatusCode, readAll(t, resp))
	}

	resp = doRequest(t, client, http.MethodGet, server.URL+"/v1/positions/loc-http/sku-http", tenant, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("position status = %d", resp.StatusCode)
	}
	position = decodeBody[Position](t, resp)
	if position.OnHand != 3 || position.Reserved != 0 || position.InventoryValueCents != 300 {
		t.Fatalf("position after confirm = %+v", position)
	}

	resp = doRequest(t, client, http.MethodGet, server.URL+"/v1/reconciliation", tenant, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reconciliation status = %d, body = %s", resp.StatusCode, readAll(t, resp))
	}
	report := decodeBody[Reconciliation](t, resp)
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}

	resp = doRequest(t, client, http.MethodGet, server.URL+"/v1/reservations/"+reservation.ID, "tenant-other", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant status = %d, want 404", resp.StatusCode)
	}
}

func TestHTTPValidation(t *testing.T) {
	server, _ := testHTTP(t)
	client := server.Client()

	resp := doRequest(t, client, http.MethodPost, server.URL+"/v1/receipts", "", "", `{}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing tenant status = %d, want 400", resp.StatusCode)
	}

	resp = doRequest(t, client, http.MethodPost, server.URL+"/v1/receipts", "tenant", "",
		`{"location_id":"loc","sku":"sku","quantity":-1}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative quantity status = %d, want 400", resp.StatusCode)
	}

	resp = doRequest(t, client, http.MethodPost, server.URL+"/v1/receipts", "tenant", "",
		`{"location_id":"loc","sku":"sku","quantity":1,"unknown":"field"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTPInsufficientStock(t *testing.T) {
	server, _ := testHTTP(t)
	client := server.Client()
	tenant := "tenant-stock"

	resp := doRequest(t, client, http.MethodPost, server.URL+"/v1/reservations", tenant, "",
		`{"location_id":"loc","sku":"sku","quantity":1}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown position status = %d, want 404", resp.StatusCode)
	}

	doRequest(t, client, http.MethodPost, server.URL+"/v1/receipts", tenant, "",
		`{"location_id":"loc","sku":"sku","quantity":1,"unit_cost_cents":10}`)

	resp = doRequest(t, client, http.MethodPost, server.URL+"/v1/reservations", tenant, "",
		`{"location_id":"loc","sku":"sku","quantity":2}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("oversell status = %d, want 409", resp.StatusCode)
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}
