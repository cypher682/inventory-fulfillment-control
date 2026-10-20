package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

type Check func(context.Context) error

type Handler struct {
	service string
	version string
	checks  map[string]Check
	mu      sync.RWMutex
}

func New(service, version string) *Handler {
	return &Handler{
		service: service,
		version: version,
		checks:  make(map[string]Check),
	}
}

func (h *Handler) AddCheck(name string, check Check) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = check
}

func (h *Handler) Liveness(w http.ResponseWriter, _ *http.Request) {
	h.write(w, http.StatusOK, "ok", nil)
}

func (h *Handler) Readiness(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	checks := make(map[string]Check, len(h.checks))
	for name, check := range h.checks {
		checks[name] = check
	}
	h.mu.RUnlock()

	results := make(map[string]string, len(checks))
	status := http.StatusOK
	state := "ok"
	for name, check := range checks {
		if err := check(r.Context()); err != nil {
			results[name] = "failed: " + err.Error()
			status = http.StatusServiceUnavailable
			state = "degraded"
			continue
		}
		results[name] = "ok"
	}

	h.write(w, status, state, results)
}

func (h *Handler) write(w http.ResponseWriter, status int, state string, checks map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"service": h.service,
		"version": h.version,
		"status":  state,
		"checks":  checks,
	})
}
