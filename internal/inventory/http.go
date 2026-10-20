package inventory

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	service *Service
	version string
	log     *slog.Logger
}

func NewServer(service *Service, version string, log *slog.Logger) *Server {
	return &Server{service: service, version: version, log: log}
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/receipts", s.receive)
	mux.HandleFunc("POST /v1/reservations", s.reserve)
	mux.HandleFunc("GET /v1/reservations/{id}", s.getReservation)
	mux.HandleFunc("POST /v1/reservations/{id}/confirm", s.confirm)
	mux.HandleFunc("POST /v1/reservations/{id}/release", s.release)
	mux.HandleFunc("GET /v1/positions/{location}/{sku}", s.getPosition)
	mux.HandleFunc("GET /v1/atp", s.atp)
	mux.HandleFunc("POST /v1/transfers", s.createTransfer)
	mux.HandleFunc("GET /v1/transfers/{id}", s.getTransfer)
	mux.HandleFunc("POST /v1/transfers/{id}/receive", s.receiveTransfer)
	mux.HandleFunc("GET /v1/reconciliation", s.reconcile)
	mux.HandleFunc("GET /metrics", s.metrics)
}

func (s *Server) receive(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	var in ReceiveInput
	if !decode(w, r, &in) {
		return
	}
	position, replay, err := s.service.Receive(r.Context(), tenant, actor(r), key(r), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeMutation(w, http.StatusCreated, replay, position)
}

func (s *Server) reserve(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	var in ReserveInput
	if !decode(w, r, &in) {
		return
	}
	reservation, replay, err := s.service.Reserve(r.Context(), tenant, actor(r), key(r), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeMutation(w, http.StatusCreated, replay, reservation)
}

func (s *Server) confirm(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	reservation, replay, err := s.service.Confirm(r.Context(), tenant, actor(r), key(r), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeMutation(w, http.StatusOK, replay, reservation)
}

func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	reservation, replay, err := s.service.Release(r.Context(), tenant, actor(r), key(r), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeMutation(w, http.StatusOK, replay, reservation)
}

func (s *Server) getReservation(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	reservation, err := s.service.GetReservation(r.Context(), tenant, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reservation)
}

func (s *Server) getPosition(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	position, err := s.service.GetPosition(r.Context(), tenant, r.PathValue("location"), r.PathValue("sku"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, position)
}

func (s *Server) atp(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	location := strings.TrimSpace(r.URL.Query().Get("location"))
	sku := strings.TrimSpace(r.URL.Query().Get("sku"))
	if location == "" || sku == "" {
		writeError(w, http.StatusBadRequest, "validation", "location and sku are required")
		return
	}
	result, err := s.service.ATP(r.Context(), tenant, location, sku)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) createTransfer(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	var in TransferInput
	if !decode(w, r, &in) {
		return
	}
	transfer, replay, err := s.service.CreateTransfer(r.Context(), tenant, actor(r), key(r), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeMutation(w, http.StatusCreated, replay, transfer)
}

func (s *Server) getTransfer(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	transfer, err := s.service.GetTransfer(r.Context(), tenant, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transfer)
}

func (s *Server) receiveTransfer(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	transfer, replay, err := s.service.ReceiveTransfer(r.Context(), tenant, actor(r), key(r), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeMutation(w, http.StatusOK, replay, transfer)
}

func (s *Server) reconcile(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	result, err := s.service.Reconcile(r.Context(), tenant)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if !result.OK {
		status = http.StatusConflict
	}
	writeJSON(w, status, result)
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	s.service.metrics.writePrometheus(w)
}

func (s *Server) tenant(w http.ResponseWriter, r *http.Request) (string, bool) {
	tenant := strings.TrimSpace(r.Header.Get("X-Tenant-ID"))
	if tenant == "" {
		writeError(w, http.StatusBadRequest, "tenant_required", "X-Tenant-ID header is required")
		return "", false
	}
	if len(tenant) > 64 {
		writeError(w, http.StatusBadRequest, "validation", "X-Tenant-ID must be at most 64 characters")
		return "", false
	}
	return tenant, true
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var validation *ValidationError
	switch {
	case errors.As(err, &validation):
		writeError(w, http.StatusBadRequest, "validation", validation.Error())
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, ErrInsufficientStock):
		writeError(w, http.StatusConflict, "insufficient_stock", err.Error())
	case errors.Is(err, ErrInvalidState):
		writeError(w, http.StatusConflict, "invalid_state", err.Error())
	case errors.Is(err, ErrIdempotencyMismatch):
		writeError(w, http.StatusUnprocessableEntity, "idempotency_mismatch", err.Error())
	case errors.Is(err, ErrRequestInProgress):
		writeError(w, http.StatusConflict, "request_in_progress", err.Error())
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func (s *Server) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("panic recovered", "method", r.Method, "path", r.URL.Path, "panic", recovered)
				if rec.status == http.StatusOK {
					writeError(rec, http.StatusInternalServerError, "internal", "internal server error")
				}
			}
			s.log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		}()

		next.ServeHTTP(rec, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body: "+err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "body must contain a single JSON object")
		return false
	}
	return true
}

func writeMutation(w http.ResponseWriter, status int, replay bool, body any) {
	if replay {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func key(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("X-Idempotency-Key"))
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func actor(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("X-Actor"))
	if value == "" {
		return "api"
	}
	if len(value) > 64 {
		return value[:64]
	}
	return value
}
