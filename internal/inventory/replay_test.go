package inventory

import (
	"strings"
	"testing"
)

func TestReplayRebuildsPositionsFromMovementLog(t *testing.T) {
	svc, pool := testService(t)
	tenant := "tenant-replay"

	mustReceive(t, svc, tenant, "loc-a", "sku-1", 10, 100)
	reservation := mustReserve(t, svc, tenant, "loc-a", "sku-1", 3)
	if _, _, err := svc.Confirm(t.Context(), tenant, "test", "", reservation.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	transfer, _, err := svc.CreateTransfer(t.Context(), tenant, "test", "", TransferInput{
		FromLocationID: "loc-a",
		ToLocationID:   "loc-b",
		SKU:            "sku-1",
		Quantity:       2,
	})
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if _, _, err := svc.ReceiveTransfer(t.Context(), tenant, "test", "", transfer.ID); err != nil {
		t.Fatalf("receive transfer: %v", err)
	}
	if _, _, err := svc.Adjust(t.Context(), tenant, "test", "", AdjustmentInput{
		LocationID:    "loc-a",
		SKU:           "sku-1",
		QuantityDelta: -1,
		Reason:        "damaged",
	}); err != nil {
		t.Fatalf("adjust: %v", err)
	}

	report, err := svc.Replay(t.Context(), tenant)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !report.OK {
		t.Fatalf("replay issues: %v", report.Issues)
	}
	if report.MovementCount != 5 {
		t.Fatalf("movement count = %d, want 5", report.MovementCount)
	}
	if len(report.Positions) != 2 {
		t.Fatalf("positions = %d, want 2", len(report.Positions))
	}
	if p := report.Positions[0]; p.LocationID != "loc-a" || p.ReplayedOnHand != 4 || p.ReplayedValueCents != 400 {
		t.Fatalf("loc-a = %+v", p)
	}
	if p := report.Positions[1]; p.LocationID != "loc-b" || p.ReplayedOnHand != 2 || p.ReplayedValueCents != 200 {
		t.Fatalf("loc-b = %+v", p)
	}

	if _, err := pool.Exec(t.Context(),
		`UPDATE positions SET on_hand = on_hand + 1 WHERE tenant_id = $1 AND location_id = 'loc-a' AND sku = 'sku-1'`,
		tenant,
	); err != nil {
		t.Fatalf("corrupt position: %v", err)
	}

	report, err = svc.Replay(t.Context(), tenant)
	if err != nil {
		t.Fatalf("replay after corruption: %v", err)
	}
	if report.OK {
		t.Fatal("replay = ok, want drift")
	}
	if len(report.Issues) != 1 || !strings.Contains(report.Issues[0], "position drift for loc-a/sku-1") {
		t.Fatalf("issues = %v", report.Issues)
	}
}

func TestReplayRejectsUnknownTenant(t *testing.T) {
	svc, _ := testService(t)
	if _, err := svc.Replay(t.Context(), ""); err == nil {
		t.Fatal("replay with empty tenant succeeded")
	}
}
