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

const (
	testTenantKey = "0123456789abcdef"
	testOtherKey  = "fedcba9876543210"
)

func authTestServer(t *testing.T, required bool, keys map[string]string) (*httptest.Server, *Server) {
	t.Helper()
	svc := NewService(nil)
	api := NewServer(svc, "test", slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuth(required, keys)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/whoami", func(w http.ResponseWriter, r *http.Request) {
		tenant, _ := tenantFromContext(r.Context())
		writeJSON(w, http.StatusOK, map[string]string{"tenant": tenant})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(api.Handler(mux))
	t.Cleanup(server.Close)
	return server, api
}

func authRequest(t *testing.T, client *http.Client, url, apiKey, bearer, tenant string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if tenant != "" {
		req.Header.Set("X-Tenant-ID", tenant)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body := make(map[string]any)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode error body: %v", err)
		}
	}
	return resp, body
}

func TestAuthRequiresValidKey(t *testing.T) {
	server, api := authTestServer(t, true, map[string]string{"demo": testTenantKey})
	client := server.Client()

	resp, body := authRequest(t, client, server.URL+"/v1/whoami", "", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d, want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q", resp.Header.Get("WWW-Authenticate"))
	}
	if errorCode(body) != "unauthorized" {
		t.Fatalf("error = %v", body)
	}

	resp, _ = authRequest(t, client, server.URL+"/v1/whoami", testOtherKey, "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key status = %d, want 401", resp.StatusCode)
	}
	if got := api.service.metrics.authFailures.Load(); got != 2 {
		t.Fatalf("auth failures = %d, want 2", got)
	}
}

func TestAuthBindsTenantToKey(t *testing.T) {
	server, _ := authTestServer(t, true, map[string]string{
		"demo": testTenantKey,
		"acme": testOtherKey,
	})
	client := server.Client()

	for _, tc := range []struct {
		name   string
		apiKey string
		bearer string
		tenant string
	}{
		{name: "x-api-key", apiKey: testTenantKey},
		{name: "bearer", bearer: testOtherKey},
		{name: "matching header", apiKey: testTenantKey, tenant: "demo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := authRequest(t, client, server.URL+"/v1/whoami", tc.apiKey, tc.bearer, tc.tenant)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			defer resp.Body.Close()
			var body struct {
				Tenant string `json:"tenant"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			want := "demo"
			if tc.bearer != "" {
				want = "acme"
			}
			if body.Tenant != want {
				t.Fatalf("tenant = %q, want %q", body.Tenant, want)
			}
		})
	}

	resp, body := authRequest(t, client, server.URL+"/v1/whoami", testTenantKey, "", "acme")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tenant mismatch status = %d, want 403", resp.StatusCode)
	}
	if errorCode(body) != "tenant_mismatch" {
		t.Fatalf("error = %v", body)
	}
}

func TestAuthLeavesHealthOpenAndCanBeDisabled(t *testing.T) {
	server, _ := authTestServer(t, true, map[string]string{"demo": testTenantKey})
	resp, _ := authRequest(t, server.Client(), server.URL+"/healthz", "", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	openServer, _ := authTestServer(t, false, nil)
	resp, _ = authRequest(t, openServer.Client(), openServer.URL+"/v1/whoami", "", "", "demo")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth disabled status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHTTPAuthProtectsInventoryRoutes(t *testing.T) {
	svc, _ := testService(t)
	api := NewServer(svc, "test", slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuth(true, map[string]string{
		"demo": testTenantKey,
		"acme": testOtherKey,
	})
	mux := http.NewServeMux()
	api.Register(mux)
	server := httptest.NewServer(api.Handler(mux))
	defer server.Close()

	request := func(method, path, key, tenant, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		if tenant != "" {
			req.Header.Set("X-Tenant-ID", tenant)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		return resp
	}

	resp := request(http.MethodPost, "/v1/receipts", testTenantKey, "",
		`{"location_id":"loc-auth","sku":"sku-auth","quantity":5,"unit_cost_cents":100}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("receipt with key status = %d, body = %s", resp.StatusCode, readAll(t, resp))
	}
	resp.Body.Close()

	resp = request(http.MethodGet, "/v1/positions/loc-auth/sku-auth", testTenantKey, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("position with key status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = request(http.MethodGet, "/v1/positions/loc-auth/sku-auth", testOtherKey, "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant position status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	resp = request(http.MethodPost, "/v1/receipts", "", "", `{}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	resp = request(http.MethodGet, "/v1/positions/loc-auth/sku-auth", testTenantKey, "acme", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mismatched tenant status = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

func errorCode(body map[string]any) string {
	values, _ := body["error"].(map[string]any)
	code, _ := values["code"].(string)
	return code
}
