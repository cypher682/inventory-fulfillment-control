package inventory

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cypher682/inventory-fulfillment-control/internal/outbox"
)

func TestExpireReservationsRestoresStock(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-expiry"
	mustReceive(t, svc, tenant, "loc-1", "sku-1", 10, 100)
	reservation := mustReserve(t, svc, tenant, "loc-1", "sku-1", 4)

	svc.clock = func() time.Time { return time.Now().Add(20 * time.Minute) }

	expired, err := svc.ExpireReservations(t.Context(), 10)
	if err != nil {
		t.Fatalf("expire reservations: %v", err)
	}
	if expired != 1 {
		t.Fatalf("expired = %d, want 1", expired)
	}

	stored, err := svc.GetReservation(t.Context(), tenant, reservation.ID)
	if err != nil {
		t.Fatalf("get reservation: %v", err)
	}
	if stored.Status != "expired" {
		t.Fatalf("status = %q, want expired", stored.Status)
	}

	position, err := svc.GetPosition(t.Context(), tenant, "loc-1", "sku-1")
	if err != nil {
		t.Fatalf("get position: %v", err)
	}
	if position.Reserved != 0 || position.Available != 10 {
		t.Fatalf("position = %+v, want reserved=0 available=10", position)
	}

	if _, _, err := svc.Confirm(t.Context(), tenant, "tester", "", reservation.ID); !errors.Is(err, ErrReservationExpired) {
		t.Fatalf("confirm expired reservation error = %v, want ErrReservationExpired", err)
	}

	again, err := svc.ExpireReservations(t.Context(), 10)
	if err != nil {
		t.Fatalf("second expire: %v", err)
	}
	if again != 0 {
		t.Fatalf("second expire = %d, want 0", again)
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestATPReportsExpiredReservations(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-atp-expiry"
	base := time.Now()
	svc.clock = func() time.Time { return base }

	mustReceive(t, svc, tenant, "loc-1", "sku-1", 10, 100)
	mustReserve(t, svc, tenant, "loc-1", "sku-1", 4)

	svc.clock = func() time.Time { return base.Add(20 * time.Minute) }

	atp, err := svc.ATP(t.Context(), tenant, "loc-1", "sku-1")
	if err != nil {
		t.Fatalf("atp: %v", err)
	}
	if atp.Reserved != 4 || atp.ExpiredReserved != 4 || atp.Available != 10 {
		t.Fatalf("atp = %+v, want reserved=4 expired_reserved=4 available=10", atp)
	}

	if _, err := svc.ExpireReservations(t.Context(), 10); err != nil {
		t.Fatalf("expire: %v", err)
	}

	atp, err = svc.ATP(t.Context(), tenant, "loc-1", "sku-1")
	if err != nil {
		t.Fatalf("atp after sweep: %v", err)
	}
	if atp.Reserved != 0 || atp.ExpiredReserved != 0 || atp.Available != 10 {
		t.Fatalf("atp after sweep = %+v, want reserved=0 expired_reserved=0 available=10", atp)
	}
}

func TestAdjustmentsPreserveValueAndLedger(t *testing.T) {
	svc, pool := testService(t)
	tenant := "tenant-adjust"
	mustReceive(t, svc, tenant, "loc-1", "sku-1", 10, 500)

	loss, _, err := svc.Adjust(t.Context(), tenant, "tester", "adjust-1", AdjustmentInput{
		LocationID:    "loc-1",
		SKU:           "sku-1",
		QuantityDelta: -2,
		Reason:        "damaged in handling",
	})
	if err != nil {
		t.Fatalf("negative adjustment: %v", err)
	}
	if loss.Position.OnHand != 8 || loss.Position.InventoryValueCents != 4000 || loss.ValueDeltaCents != -1000 {
		t.Fatalf("loss = %+v", loss)
	}

	gain, _, err := svc.Adjust(t.Context(), tenant, "tester", "adjust-2", AdjustmentInput{
		LocationID:    "loc-1",
		SKU:           "sku-1",
		QuantityDelta: 3,
		UnitCostCents: 600,
		Reason:        "cycle count surplus",
	})
	if err != nil {
		t.Fatalf("positive adjustment: %v", err)
	}
	if gain.Position.OnHand != 11 || gain.Position.InventoryValueCents != 5800 || gain.ValueDeltaCents != 1800 {
		t.Fatalf("gain = %+v", gain)
	}

	replay, isReplay, err := svc.Adjust(t.Context(), tenant, "tester", "adjust-1", AdjustmentInput{
		LocationID:    "loc-1",
		SKU:           "sku-1",
		QuantityDelta: -2,
		Reason:        "damaged in handling",
	})
	if err != nil || !isReplay || replay.ID != loss.ID {
		t.Fatalf("idempotent adjustment: id=%q replay=%v err=%v", replay.ID, isReplay, err)
	}
	if got := scalarInt64(t, pool, `SELECT COUNT(*) FROM adjustments WHERE tenant_id = $1`, tenant); got != 2 {
		t.Fatalf("adjustments = %d, want 2", got)
	}

	if got := scalarInt64(t, pool,
		`SELECT COALESCE(SUM(debit_cents - credit_cents), 0) FROM ledger_entries WHERE tenant_id = $1 AND account = 'inventory'`, tenant,
	); got != 5800 {
		t.Fatalf("inventory ledger = %d, want 5800", got)
	}
	if got := scalarInt64(t, pool,
		`SELECT COALESCE(SUM(debit_cents - credit_cents), 0) FROM ledger_entries WHERE tenant_id = $1`, tenant,
	); got != 0 {
		t.Fatalf("ledger imbalance = %d", got)
	}

	mustReserve(t, svc, tenant, "loc-1", "sku-1", 5)
	_, _, err = svc.Adjust(t.Context(), tenant, "tester", "", AdjustmentInput{
		LocationID:    "loc-1",
		SKU:           "sku-1",
		QuantityDelta: -7,
		Reason:        "write off",
	})
	if !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("adjustment into reserved stock error = %v, want ErrInsufficientStock", err)
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestAuditQueriesAreTenantScopedAndPaginated(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-audit"
	mustReceive(t, svc, tenant, "loc-1", "sku-1", 10, 100)
	reservation := mustReserve(t, svc, tenant, "loc-1", "sku-1", 2)
	if _, _, err := svc.Confirm(t.Context(), tenant, "tester", "", reservation.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, _, err := svc.Adjust(t.Context(), tenant, "tester", "", AdjustmentInput{
		LocationID:    "loc-1",
		SKU:           "sku-1",
		QuantityDelta: -1,
		Reason:        "count correction",
	}); err != nil {
		t.Fatalf("adjustment: %v", err)
	}

	first, err := svc.ListEvents(t.Context(), tenant, EventFilter{Limit: 2})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(first.Events) != 2 || !first.HasMore || first.NextAfter != first.Events[1].ID {
		t.Fatalf("first page = %+v", first)
	}

	second, err := svc.ListEvents(t.Context(), tenant, EventFilter{After: first.NextAfter, Limit: 10})
	if err != nil {
		t.Fatalf("list events page 2: %v", err)
	}
	if len(second.Events) == 0 || second.Events[0].ID <= first.NextAfter {
		t.Fatalf("second page = %+v", second)
	}

	reservationEvents, err := svc.ListEvents(t.Context(), tenant, EventFilter{
		AggregateType: "reservation",
		AggregateID:   reservation.ID,
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("filtered events: %v", err)
	}
	if len(reservationEvents.Events) != 2 {
		t.Fatalf("reservation events = %d, want 2", len(reservationEvents.Events))
	}

	other, err := svc.ListEvents(t.Context(), "tenant-other", EventFilter{Limit: 10})
	if err != nil {
		t.Fatalf("tenant isolation events: %v", err)
	}
	if len(other.Events) != 0 || other.HasMore {
		t.Fatalf("cross-tenant events = %+v", other)
	}

	movements, err := svc.ListMovements(t.Context(), tenant, MovementFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list movements: %v", err)
	}
	if len(movements.Movements) != 3 {
		t.Fatalf("movements = %d, want 3", len(movements.Movements))
	}

	ledger, err := svc.ListLedger(t.Context(), tenant, LedgerFilter{Account: "inventory", Limit: 10})
	if err != nil {
		t.Fatalf("list ledger: %v", err)
	}
	if len(ledger.Entries) != 3 {
		t.Fatalf("inventory ledger entries = %d, want 3", len(ledger.Entries))
	}
	if _, err := svc.ListEvents(t.Context(), tenant, EventFilter{
		AggregateType: string(make([]byte, 65)),
	}); err == nil {
		t.Fatal("expected validation error for oversized filter")
	}
}

func TestHTTPAdjustmentsAuditAndExpiry(t *testing.T) {
	server, svc := testHTTP(t)
	client := server.Client()
	tenant := "tenant-http-ops"

	resp := doRequest(t, client, http.MethodPost, server.URL+"/v1/receipts", tenant, "receipt-ops",
		`{"location_id":"loc-ops","sku":"sku-ops","quantity":10,"unit_cost_cents":250}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("receipt status = %d, body = %s", resp.StatusCode, readAll(t, resp))
	}

	resp = doRequest(t, client, http.MethodPost, server.URL+"/v1/adjustments", tenant, "adjust-ops",
		`{"location_id":"loc-ops","sku":"sku-ops","quantity_delta":-3,"reason":"cycle count"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("adjustment status = %d, body = %s", resp.StatusCode, readAll(t, resp))
	}
	adjustment := decodeBody[Adjustment](t, resp)
	if adjustment.Position.OnHand != 7 || adjustment.ValueDeltaCents != -750 {
		t.Fatalf("adjustment = %+v", adjustment)
	}

	resp = doRequest(t, client, http.MethodGet, server.URL+"/v1/audit/events?aggregate_type=adjustment&limit=10", tenant, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit events status = %d", resp.StatusCode)
	}
	events := decodeBody[EventPage](t, resp)
	if len(events.Events) != 1 || events.Events[0].EventType != "inventory.adjusted" {
		t.Fatalf("events = %+v", events)
	}

	resp = doRequest(t, client, http.MethodGet, server.URL+"/v1/audit/movements?movement_type=adjustment_out", tenant, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit movements status = %d", resp.StatusCode)
	}
	if movements := decodeBody[MovementPage](t, resp); len(movements.Movements) != 1 {
		t.Fatalf("movements = %+v", movements)
	}

	resp = doRequest(t, client, http.MethodGet, server.URL+"/v1/audit/ledger?account=inventory", tenant, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit ledger status = %d", resp.StatusCode)
	}
	if ledger := decodeBody[LedgerPage](t, resp); len(ledger.Entries) != 2 {
		t.Fatalf("ledger = %+v", ledger)
	}

	resp = doRequest(t, client, http.MethodGet, server.URL+"/v1/audit/events?limit=201", tenant, "", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid limit status = %d, want 400", resp.StatusCode)
	}

	resp = doRequest(t, client, http.MethodGet, server.URL+"/v1/audit/events?after=abc", tenant, "", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid after status = %d, want 400", resp.StatusCode)
	}

	resp = doRequest(t, client, http.MethodGet, server.URL+"/metrics", tenant, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d", resp.StatusCode)
	}
	metricsBody := readAll(t, resp)
	for _, want := range []string{
		"inventory_adjustments_total 1",
		"inventory_http_requests_total",
		"inventory_http_request_duration_seconds_bucket",
		"inventory_outbox_unpublished",
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, metricsBody)
		}
	}

	resp = doRequest(t, client, http.MethodPost, server.URL+"/v1/reservations", tenant, "reserve-expiry",
		`{"location_id":"loc-ops","sku":"sku-ops","quantity":2,"ttl_seconds":60}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("reserve status = %d, body = %s", resp.StatusCode, readAll(t, resp))
	}
	reservation := decodeBody[Reservation](t, resp)
	svc.clock = func() time.Time { return time.Now().Add(2 * time.Minute) }

	resp = doRequest(t, client, http.MethodPost, server.URL+"/v1/reservations/"+reservation.ID+"/confirm", tenant, "", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expired confirm status = %d, want 409", resp.StatusCode)
	}
	body := decodeBody[map[string]map[string]string](t, resp)
	if body["error"]["code"] != "reservation_expired" {
		t.Fatalf("error = %+v", body)
	}
}

func TestOutboxRelayPublishesAndRetries(t *testing.T) {
	svc, pool := testService(t)
	tenant := "tenant-outbox"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	mustReceive(t, svc, tenant, "loc-1", "sku-1", 1, 100)

	var delivered []outbox.Message
	relay := outbox.NewRelay(pool, outbox.PublisherFunc(func(_ context.Context, m outbox.Message) error {
		delivered = append(delivered, m)
		return nil
	}), log).WithBatch(10)

	published, err := relay.Drain(t.Context())
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if published != 1 || len(delivered) != 1 {
		t.Fatalf("published=%d delivered=%d, want 1/1", published, len(delivered))
	}
	if got := scalarInt64(t, pool, `SELECT COUNT(*) FROM outbox WHERE tenant_id = $1 AND published_at IS NULL`, tenant); got != 0 {
		t.Fatalf("unpublished = %d, want 0", got)
	}

	mustReceive(t, svc, tenant, "loc-2", "sku-1", 1, 100)
	failing := outbox.NewRelay(pool, outbox.PublisherFunc(func(context.Context, outbox.Message) error {
		return errors.New("broker unavailable")
	}), log).WithBatch(10)

	if _, err := failing.Drain(t.Context()); err == nil {
		t.Fatal("drain with failing publisher = nil error")
	}
	if got := scalarInt64(t, pool,
		`SELECT COUNT(*) FROM outbox WHERE tenant_id = $1 AND published_at IS NULL AND last_error IS NOT NULL`, tenant,
	); got != 1 {
		t.Fatalf("failed rows = %d, want 1", got)
	}

	if _, err := pool.Exec(t.Context(),
		`UPDATE outbox SET next_attempt_at = now() WHERE tenant_id = $1 AND published_at IS NULL`, tenant,
	); err != nil {
		t.Fatalf("make eligible: %v", err)
	}
	published, err = relay.Drain(t.Context())
	if err != nil {
		t.Fatalf("retry drain: %v", err)
	}
	if published != 1 {
		t.Fatalf("retry published = %d, want 1", published)
	}
	if got := scalarInt64(t, pool, `SELECT COUNT(*) FROM outbox WHERE tenant_id = $1 AND published_at IS NULL`, tenant); got != 0 {
		t.Fatalf("unpublished after retry = %d, want 0", got)
	}
}
