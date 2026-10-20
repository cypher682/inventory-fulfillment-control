package inventory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cypher682/inventory-fulfillment-control/internal/store"
)

func testService(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; start deploy/docker-compose.yml and set it to run integration tests")
	}

	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Fatalf("ping admin pool: %v", err)
	}

	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		admin.Close()
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 30

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		admin.Close()
		t.Fatalf("create pool: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
		admin.Close()
	})

	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return NewService(pool), pool
}

func mustReceive(t *testing.T, svc *Service, tenant, location, sku string, quantity, unitCost int64) Position {
	t.Helper()
	p, _, err := svc.Receive(t.Context(), tenant, "test", "", ReceiveInput{
		LocationID:    location,
		SKU:           sku,
		Quantity:      quantity,
		UnitCostCents: unitCost,
	})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	return p
}

func mustReserve(t *testing.T, svc *Service, tenant, location, sku string, quantity int64) Reservation {
	t.Helper()
	r, _, err := svc.Reserve(t.Context(), tenant, "test", "", ReserveInput{
		LocationID: location,
		SKU:        sku,
		Quantity:   quantity,
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	return r
}

func scalarInt64(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int64 {
	t.Helper()
	var value int64
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

func TestReceiveIsIdempotent(t *testing.T) {
	svc, pool := testService(t)
	tenant := "tenant-a"
	in := ReceiveInput{LocationID: "loc-1", SKU: "sku-1", Quantity: 10, UnitCostCents: 250}

	first, replay, err := svc.Receive(t.Context(), tenant, "tester", "key-1", in)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if replay {
		t.Fatal("first call reported replay")
	}
	if first.OnHand != 10 || first.Available != 10 || first.InventoryValueCents != 2500 {
		t.Fatalf("unexpected position: %+v", first)
	}

	second, replay, err := svc.Receive(t.Context(), tenant, "tester", "key-1", in)
	if err != nil {
		t.Fatalf("replay receive: %v", err)
	}
	if !replay {
		t.Fatal("second call did not report replay")
	}
	if second.OnHand != 10 {
		t.Fatalf("replay changed on_hand: %+v", second)
	}
	if got := scalarInt64(t, pool, `SELECT COUNT(*) FROM inventory_movements WHERE tenant_id = $1`, tenant); got != 1 {
		t.Fatalf("movements = %d, want 1", got)
	}
	if got := scalarInt64(t, pool, `SELECT COUNT(*) FROM ledger_entries WHERE tenant_id = $1`, tenant); got != 2 {
		t.Fatalf("ledger entries = %d, want 2", got)
	}

	_, _, err = svc.Receive(t.Context(), tenant, "tester", "key-1", ReceiveInput{
		LocationID: "loc-1", SKU: "sku-1", Quantity: 5, UnitCostCents: 250,
	})
	if !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("error = %v, want ErrIdempotencyMismatch", err)
	}
}

func TestConcurrentReservationsDoNotOversell(t *testing.T) {
	svc, pool := testService(t)
	tenant := "tenant-hot"
	mustReceive(t, svc, tenant, "loc-hot", "sku-hot", 100, 100)

	const attempts = 250
	var success, rejected atomic.Int64
	errs := make(chan error, attempts)

	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			_, _, err := svc.Reserve(t.Context(), tenant, "load-test", "", ReserveInput{
				LocationID: "loc-hot",
				SKU:        "sku-hot",
				Quantity:   1,
			})
			switch {
			case err == nil:
				success.Add(1)
			case errors.Is(err, ErrInsufficientStock):
				rejected.Add(1)
			default:
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("unexpected reservation error: %v", err)
	}
	if got := success.Load(); got != 100 {
		t.Fatalf("successful reservations = %d, want 100", got)
	}
	if got := rejected.Load(); got != attempts-100 {
		t.Fatalf("rejections = %d, want %d", got, attempts-100)
	}

	p, err := svc.GetPosition(t.Context(), tenant, "loc-hot", "sku-hot")
	if err != nil {
		t.Fatalf("get position: %v", err)
	}
	if p.OnHand != 100 || p.Reserved != 100 || p.Available != 0 {
		t.Fatalf("position after load: %+v", p)
	}
	if got := scalarInt64(t, pool, `SELECT COUNT(*) FROM reservations WHERE tenant_id = $1 AND status = 'active'`, tenant); got != 100 {
		t.Fatalf("active reservations = %d, want 100", got)
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestConfirmPostsBalancedLedger(t *testing.T) {
	svc, pool := testService(t)
	tenant := "tenant-ledger"
	mustReceive(t, svc, tenant, "loc-1", "sku-1", 10, 500)

	r := mustReserve(t, svc, tenant, "loc-1", "sku-1", 4)
	confirmed, _, err := svc.Confirm(t.Context(), tenant, "tester", "confirm-1", r.ID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Status != "confirmed" {
		t.Fatalf("status = %q, want confirmed", confirmed.Status)
	}

	p, err := svc.GetPosition(t.Context(), tenant, "loc-1", "sku-1")
	if err != nil {
		t.Fatalf("get position: %v", err)
	}
	if p.OnHand != 6 || p.Reserved != 0 || p.InventoryValueCents != 3000 {
		t.Fatalf("position = %+v", p)
	}

	if got := scalarInt64(t, pool,
		`SELECT COALESCE(SUM(debit_cents - credit_cents), 0) FROM ledger_entries WHERE tenant_id = $1`, tenant,
	); got != 0 {
		t.Fatalf("ledger imbalance = %d", got)
	}
	if got := scalarInt64(t, pool,
		`SELECT COALESCE(SUM(debit_cents - credit_cents), 0) FROM ledger_entries WHERE tenant_id = $1 AND account = 'inventory'`, tenant,
	); got != 3000 {
		t.Fatalf("inventory ledger = %d, want 3000", got)
	}
	if got := scalarInt64(t, pool,
		`SELECT COALESCE(SUM(debit_cents - credit_cents), 0) FROM ledger_entries WHERE tenant_id = $1 AND account = 'cogs'`, tenant,
	); got != 2000 {
		t.Fatalf("cogs ledger = %d, want 2000", got)
	}

	if _, replay, err := svc.Confirm(t.Context(), tenant, "tester", "confirm-1", r.ID); err != nil || !replay {
		t.Fatalf("idempotent confirm: replay=%v err=%v", replay, err)
	}
	if got := scalarInt64(t, pool, `SELECT COUNT(*) FROM inventory_movements WHERE tenant_id = $1 AND movement_type = 'ship'`, tenant); got != 1 {
		t.Fatalf("ship movements = %d, want 1", got)
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestReleaseRestoresAvailability(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-release"
	mustReceive(t, svc, tenant, "loc-1", "sku-1", 5, 100)

	r := mustReserve(t, svc, tenant, "loc-1", "sku-1", 5)
	released, _, err := svc.Release(t.Context(), tenant, "tester", "", r.ID)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if released.Status != "released" {
		t.Fatalf("status = %q, want released", released.Status)
	}

	p, err := svc.GetPosition(t.Context(), tenant, "loc-1", "sku-1")
	if err != nil {
		t.Fatalf("get position: %v", err)
	}
	if p.Available != 5 || p.Reserved != 0 {
		t.Fatalf("position = %+v", p)
	}

	if _, _, err := svc.Confirm(t.Context(), tenant, "tester", "", r.ID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("confirm after release error = %v, want ErrInvalidState", err)
	}
}

func TestTransferMovesQuantityAndValue(t *testing.T) {
	svc, pool := testService(t)
	tenant := "tenant-transfer"
	mustReceive(t, svc, tenant, "loc-a", "sku-1", 10, 100)

	transfer, _, err := svc.CreateTransfer(t.Context(), tenant, "tester", "", TransferInput{
		FromLocationID: "loc-a",
		ToLocationID:   "loc-b",
		SKU:            "sku-1",
		Quantity:       3,
	})
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	if transfer.ValueCents != 300 || transfer.Status != "in_transit" {
		t.Fatalf("transfer = %+v", transfer)
	}

	source, err := svc.GetPosition(t.Context(), tenant, "loc-a", "sku-1")
	if err != nil {
		t.Fatalf("source position: %v", err)
	}
	if source.OnHand != 7 || source.InventoryValueCents != 700 {
		t.Fatalf("source = %+v", source)
	}

	atp, err := svc.ATP(t.Context(), tenant, "loc-b", "sku-1")
	if err != nil {
		t.Fatalf("atp: %v", err)
	}
	if atp.Found || atp.Available != 0 || atp.InTransit != 3 {
		t.Fatalf("atp before receipt = %+v", atp)
	}

	received, _, err := svc.ReceiveTransfer(t.Context(), tenant, "tester", "", transfer.ID)
	if err != nil {
		t.Fatalf("receive transfer: %v", err)
	}
	if received.Status != "received" || received.ReceivedAt == nil {
		t.Fatalf("received transfer = %+v", received)
	}

	destination, err := svc.GetPosition(t.Context(), tenant, "loc-b", "sku-1")
	if err != nil {
		t.Fatalf("destination position: %v", err)
	}
	if destination.OnHand != 3 || destination.InventoryValueCents != 300 {
		t.Fatalf("destination = %+v", destination)
	}

	if got := scalarInt64(t, pool,
		`SELECT COALESCE(SUM(value_cents), 0) FROM transfers WHERE tenant_id = $1 AND status = 'in_transit'`, tenant,
	); got != 0 {
		t.Fatalf("in-transit value = %d, want 0", got)
	}
	if got := scalarInt64(t, pool,
		`SELECT COALESCE(SUM(debit_cents - credit_cents), 0) FROM ledger_entries WHERE tenant_id = $1 AND account = 'inventory_in_transit'`, tenant,
	); got != 0 {
		t.Fatalf("in-transit ledger = %d, want 0", got)
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestTenantIsolation(t *testing.T) {
	svc, _ := testService(t)
	mustReceive(t, svc, "tenant-a", "loc-1", "sku-1", 1, 100)

	if _, err := svc.GetPosition(t.Context(), "tenant-b", "loc-1", "sku-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant position error = %v, want ErrNotFound", err)
	}
	if _, err := svc.GetReservation(t.Context(), "tenant-b", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant reservation error = %v, want ErrNotFound", err)
	}
}
