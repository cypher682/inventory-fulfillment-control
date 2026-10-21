package inventory

import (
	"sync"
	"testing"
)

func TestFIFOConfirmConsumesOldestLayers(t *testing.T) {
	svc, pool := testService(t)
	tenant := "tenant-fifo-confirm"

	mustReceive(t, svc, tenant, "loc-1", "sku-1", 5, 100)
	mustReceive(t, svc, tenant, "loc-1", "sku-1", 5, 200)

	position, err := svc.GetPosition(t.Context(), tenant, "loc-1", "sku-1")
	if err != nil {
		t.Fatalf("position: %v", err)
	}
	if position.InventoryValueCents != 1500 {
		t.Fatalf("position value = %d, want 1500", position.InventoryValueCents)
	}

	reservation := mustReserve(t, svc, tenant, "loc-1", "sku-1", 7)
	if _, _, err := svc.Confirm(t.Context(), tenant, "tester", "", reservation.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	position, err = svc.GetPosition(t.Context(), tenant, "loc-1", "sku-1")
	if err != nil {
		t.Fatalf("position after confirm: %v", err)
	}
	if position.OnHand != 3 || position.InventoryValueCents != 600 {
		t.Fatalf("position after confirm = %+v, want on_hand=3 value=600", position)
	}

	layers, err := svc.ListCostLayers(t.Context(), tenant, CostLayerFilter{IncludeEmpty: true, Limit: 10})
	if err != nil {
		t.Fatalf("list layers: %v", err)
	}
	if len(layers.Layers) != 2 {
		t.Fatalf("layers = %+v", layers.Layers)
	}
	if layers.Layers[0].RemainingQuantity != 0 || layers.Layers[0].RemainingValueCents != 0 {
		t.Fatalf("oldest layer = %+v, want fully consumed", layers.Layers[0])
	}
	if layers.Layers[1].RemainingQuantity != 3 || layers.Layers[1].RemainingValueCents != 600 {
		t.Fatalf("newest layer = %+v, want remaining 3/600", layers.Layers[1])
	}

	consumptions, err := svc.ListCostConsumptions(t.Context(), tenant, CostConsumptionFilter{
		RefType: "reservation",
		RefID:   reservation.ID,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("list consumptions: %v", err)
	}
	if len(consumptions.Consumptions) != 2 {
		t.Fatalf("consumptions = %+v", consumptions.Consumptions)
	}
	if consumptions.Consumptions[0].Quantity != 5 || consumptions.Consumptions[0].ValueCents != 500 {
		t.Fatalf("first consumption = %+v", consumptions.Consumptions[0])
	}
	if consumptions.Consumptions[1].Quantity != 2 || consumptions.Consumptions[1].ValueCents != 400 {
		t.Fatalf("second consumption = %+v", consumptions.Consumptions[1])
	}
	if got := scalarInt64(t, pool, `SELECT COALESCE(SUM(debit_cents - credit_cents), 0) FROM ledger_entries WHERE tenant_id = $1 AND account = 'cogs'`, tenant); got != 900 {
		t.Fatalf("cogs = %d, want 900", got)
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestFIFOTransferPreservesLayerCosts(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-fifo-transfer"

	mustReceive(t, svc, tenant, "loc-a", "sku-1", 4, 100)
	mustReceive(t, svc, tenant, "loc-a", "sku-1", 4, 300)

	transfer, _, err := svc.CreateTransfer(t.Context(), tenant, "tester", "", TransferInput{
		FromLocationID: "loc-a",
		ToLocationID:   "loc-b",
		SKU:            "sku-1",
		Quantity:       5,
	})
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	if transfer.ValueCents != 700 {
		t.Fatalf("transfer value = %d, want 700", transfer.ValueCents)
	}

	if _, _, err := svc.ReceiveTransfer(t.Context(), tenant, "tester", "", transfer.ID); err != nil {
		t.Fatalf("receive transfer: %v", err)
	}

	destination, err := svc.GetPosition(t.Context(), tenant, "loc-b", "sku-1")
	if err != nil {
		t.Fatalf("destination: %v", err)
	}
	if destination.OnHand != 5 || destination.InventoryValueCents != 700 {
		t.Fatalf("destination = %+v, want 5/700", destination)
	}

	layers, err := svc.ListCostLayers(t.Context(), tenant, CostLayerFilter{
		LocationID: "loc-b",
		SKU:        "sku-1",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("destination layers: %v", err)
	}
	if len(layers.Layers) != 2 {
		t.Fatalf("destination layers = %+v", layers.Layers)
	}
	if layers.Layers[0].RemainingQuantity != 4 || layers.Layers[0].RemainingValueCents != 400 {
		t.Fatalf("first transferred layer = %+v", layers.Layers[0])
	}
	if layers.Layers[1].RemainingQuantity != 1 || layers.Layers[1].RemainingValueCents != 300 {
		t.Fatalf("second transferred layer = %+v", layers.Layers[1])
	}

	reservation := mustReserve(t, svc, tenant, "loc-b", "sku-1", 2)
	if _, _, err := svc.Confirm(t.Context(), tenant, "tester", "", reservation.ID); err != nil {
		t.Fatalf("confirm at destination: %v", err)
	}
	destination, err = svc.GetPosition(t.Context(), tenant, "loc-b", "sku-1")
	if err != nil {
		t.Fatalf("destination after confirm: %v", err)
	}
	if destination.OnHand != 3 || destination.InventoryValueCents != 500 {
		t.Fatalf("destination after confirm = %+v, want 3/500", destination)
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestFIFONegativeAdjustmentUsesOldestLayers(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-fifo-adjust"

	mustReceive(t, svc, tenant, "loc-1", "sku-1", 3, 100)
	mustReceive(t, svc, tenant, "loc-1", "sku-1", 2, 400)

	adjustment, _, err := svc.Adjust(t.Context(), tenant, "tester", "", AdjustmentInput{
		LocationID:    "loc-1",
		SKU:           "sku-1",
		QuantityDelta: -4,
		Reason:        "shrinkage",
	})
	if err != nil {
		t.Fatalf("adjustment: %v", err)
	}
	if adjustment.ValueDeltaCents != -700 {
		t.Fatalf("adjustment value = %d, want -700", adjustment.ValueDeltaCents)
	}
	if adjustment.Position.OnHand != 1 || adjustment.Position.InventoryValueCents != 400 {
		t.Fatalf("position = %+v, want 1/400", adjustment.Position)
	}

	layers, err := svc.ListCostLayers(t.Context(), tenant, CostLayerFilter{IncludeEmpty: true, Limit: 10})
	if err != nil {
		t.Fatalf("layers: %v", err)
	}
	if len(layers.Layers) != 2 {
		t.Fatalf("layers = %+v", layers.Layers)
	}
	if layers.Layers[0].RemainingQuantity != 0 || layers.Layers[0].RemainingValueCents != 0 {
		t.Fatalf("oldest layer = %+v, want consumed", layers.Layers[0])
	}
	if layers.Layers[1].RemainingQuantity != 1 || layers.Layers[1].RemainingValueCents != 400 {
		t.Fatalf("newest layer = %+v, want 1/400", layers.Layers[1])
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestTakeLayerValuePreservesCents(t *testing.T) {
	remainingValue := int64(1001)
	remainingQuantity := int64(10)
	var consumed int64
	for _, take := range []int64{3, 4, 3} {
		value := takeLayerValue(remainingValue, remainingQuantity, take)
		consumed += value
		remainingValue -= value
		remainingQuantity -= take
	}
	if remainingQuantity != 0 || remainingValue != 0 {
		t.Fatalf("remaining quantity=%d value=%d, want 0/0", remainingQuantity, remainingValue)
	}
	if consumed != 1001 {
		t.Fatalf("consumed = %d, want 1001", consumed)
	}
	if got := takeLayerValue(1001, 10, 10); got != 1001 {
		t.Fatalf("full-layer value = %d, want 1001", got)
	}
}

func TestConcurrentConfirmationsConsumeFIFOExactly(t *testing.T) {
	svc, pool := testService(t)
	tenant := "tenant-fifo-hot"

	mustReceive(t, svc, tenant, "loc-1", "sku-1", 50, 100)
	mustReceive(t, svc, tenant, "loc-1", "sku-1", 50, 200)

	reservations := make([]Reservation, 0, 100)
	for range 100 {
		reservations = append(reservations, mustReserve(t, svc, tenant, "loc-1", "sku-1", 1))
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(reservations))
	for _, reservation := range reservations {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := svc.Confirm(t.Context(), tenant, "load-test", "", reservation.ID); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("confirm: %v", err)
	}

	position, err := svc.GetPosition(t.Context(), tenant, "loc-1", "sku-1")
	if err != nil {
		t.Fatalf("position: %v", err)
	}
	if position.OnHand != 0 || position.InventoryValueCents != 0 || position.Reserved != 0 {
		t.Fatalf("position = %+v, want empty", position)
	}
	if got := scalarInt64(t, pool,
		`SELECT COALESCE(SUM(debit_cents - credit_cents), 0) FROM ledger_entries WHERE tenant_id = $1 AND account = 'cogs'`, tenant,
	); got != 15000 {
		t.Fatalf("cogs = %d, want 15000", got)
	}
	if got := scalarInt64(t, pool,
		`SELECT COUNT(*) FROM cost_consumptions WHERE tenant_id = $1`, tenant,
	); got != 100 {
		t.Fatalf("consumptions = %d, want 100", got)
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}
