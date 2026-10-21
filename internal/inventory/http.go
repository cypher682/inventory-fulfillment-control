package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	service      *Service
	version      string
	log          *slog.Logger
	authRequired bool
	apiKeys      map[string]string
}

func NewServer(service *Service, version string, log *slog.Logger) *Server {
	return &Server{service: service, version: version, log: log}
}

func (s *Server) WithAuth(required bool, keys map[string]string) *Server {
	s.authRequired = required
	s.apiKeys = make(map[string]string, len(keys))
	for tenant, key := range keys {
		s.apiKeys[tenant] = key
	}
	return s
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/receipts", s.receive)
	mux.HandleFunc("POST /v1/adjustments", s.adjust)
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
	mux.HandleFunc("GET /v1/audit/replay", s.replay)
	mux.HandleFunc("GET /v1/audit/events", s.listEvents)
	mux.HandleFunc("GET /v1/audit/movements", s.listMovements)
	mux.HandleFunc("GET /v1/audit/ledger", s.listLedger)
	mux.HandleFunc("GET /v1/audit/cost-layers", s.listCostLayers)
	mux.HandleFunc("GET /v1/audit/cost-consumptions", s.listCostConsumptions)
	mux.HandleFunc("POST /v1/lots/hold", s.holdLot)
	mux.HandleFunc("GET /v1/lots", s.listLots)
	mux.HandleFunc("GET /v1/serials", s.listSerials)
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

func (s *Server) adjust(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	var in AdjustmentInput
	if !decode(w, r, &in) {
		return
	}
	adjustment, replay, err := s.service.Adjust(r.Context(), tenant, actor(r), key(r), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeMutation(w, http.StatusCreated, replay, adjustment)
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

func (s *Server) replay(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	report, err := s.service.Replay(r.Context(), tenant)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if !report.OK {
		status = http.StatusConflict
	}
	writeJSON(w, status, report)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	after, limit, ok := auditParams(w, r)
	if !ok {
		return
	}
	page, err := s.service.ListEvents(r.Context(), tenant, EventFilter{
		AggregateType: r.URL.Query().Get("aggregate_type"),
		AggregateID:   r.URL.Query().Get("aggregate_id"),
		EventType:     r.URL.Query().Get("event_type"),
		After:         after,
		Limit:         limit,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) listMovements(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	after, limit, ok := auditParams(w, r)
	if !ok {
		return
	}
	page, err := s.service.ListMovements(r.Context(), tenant, MovementFilter{
		LocationID:   r.URL.Query().Get("location_id"),
		SKU:          r.URL.Query().Get("sku"),
		MovementType: r.URL.Query().Get("movement_type"),
		After:        after,
		Limit:        limit,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) listLedger(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	after, limit, ok := auditParams(w, r)
	if !ok {
		return
	}
	page, err := s.service.ListLedger(r.Context(), tenant, LedgerFilter{
		Account:    r.URL.Query().Get("account"),
		LocationID: r.URL.Query().Get("location_id"),
		SKU:        r.URL.Query().Get("sku"),
		RefType:    r.URL.Query().Get("ref_type"),
		RefID:      r.URL.Query().Get("ref_id"),
		After:      after,
		Limit:      limit,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) listCostLayers(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	after, limit, ok := auditParams(w, r)
	if !ok {
		return
	}
	page, err := s.service.ListCostLayers(r.Context(), tenant, CostLayerFilter{
		LocationID:   r.URL.Query().Get("location_id"),
		SKU:          r.URL.Query().Get("sku"),
		IncludeEmpty: r.URL.Query().Get("include_empty") == "true",
		After:        after,
		Limit:        limit,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) listCostConsumptions(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	after, limit, ok := auditParams(w, r)
	if !ok {
		return
	}
	var layerID int64
	if raw := r.URL.Query().Get("layer_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "validation", "layer_id must be a positive integer")
			return
		}
		layerID = parsed
	}
	page, err := s.service.ListCostConsumptions(r.Context(), tenant, CostConsumptionFilter{
		LayerID:    layerID,
		LocationID: r.URL.Query().Get("location_id"),
		SKU:        r.URL.Query().Get("sku"),
		RefType:    r.URL.Query().Get("ref_type"),
		RefID:      r.URL.Query().Get("ref_id"),
		After:      after,
		Limit:      limit,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) holdLot(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	var in HoldLotInput
	if !decode(w, r, &in) {
		return
	}
	lot, replay, err := s.service.HoldLot(r.Context(), tenant, actor(r), key(r), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeMutation(w, http.StatusOK, replay, lot)
}

func (s *Server) listLots(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	_, limit, ok := auditParams(w, r)
	if !ok {
		return
	}
	lots, err := s.service.ListLots(r.Context(), tenant, r.URL.Query().Get("location_id"), r.URL.Query().Get("sku"), limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lots": lots})
}

func (s *Server) listSerials(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenant(w, r)
	if !ok {
		return
	}
	_, limit, ok := auditParams(w, r)
	if !ok {
		return
	}
	serials, err := s.service.ListSerials(r.Context(), tenant,
		r.URL.Query().Get("location_id"), r.URL.Query().Get("sku"),
		r.URL.Query().Get("lot_code"), r.URL.Query().Get("status"), limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"serials": serials})
}

func auditParams(w http.ResponseWriter, r *http.Request) (int64, int, bool) {
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if r.URL.Query().Get("after") == "" {
		after, err = 0, nil
	}
	if err != nil || after < 0 {
		writeError(w, http.StatusBadRequest, "validation", "after must be a non-negative integer")
		return 0, 0, false
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxAuditLimit {
			writeError(w, http.StatusBadRequest, "validation", "limit must be between 1 and 200")
			return 0, 0, false
		}
		limit = parsed
	}
	return after, limit, true
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	s.service.metrics.writePrometheus(w)
	if pending, oldest, err := s.service.OutboxPending(r.Context()); err == nil {
		fmt.Fprintf(w, "inventory_outbox_unpublished %d\n", pending)
		fmt.Fprintf(w, "inventory_outbox_oldest_age_seconds %g\n", oldest)
	}
}

func (s *Server) tenant(w http.ResponseWriter, r *http.Request) (string, bool) {
	if tenant, ok := tenantFromContext(r.Context()); ok {
		return tenant, true
	}
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
	case errors.Is(err, ErrReservationExpired):
		writeError(w, http.StatusConflict, "reservation_expired", err.Error())
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
			s.service.metrics.observeRequest(time.Since(start), rec.status)
			s.log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		}()

		if s.authRequired && strings.HasPrefix(r.URL.Path, "/v1/") {
			tenant, status := s.authenticate(r)
			if status != 0 {
				s.service.metrics.authFailures.Add(1)
				if status == http.StatusUnauthorized {
					rec.Header().Set("WWW-Authenticate", "Bearer")
					writeError(rec, status, "unauthorized", "missing or invalid API key")
				} else {
					writeError(rec, status, "tenant_mismatch", "API key is not valid for the requested tenant")
				}
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), tenantContextKey{}, tenant))
		}

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
