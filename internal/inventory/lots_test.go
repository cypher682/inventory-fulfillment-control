package inventory

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func receiveLot(t *testing.T, svc *Service, tenant, location, sku, lot string, quantity, unitCost int64, serials []string) Position {
	t.Helper()
	p, _, err := svc.Receive(t.Context(), tenant, "test", "", ReceiveInput{
		LocationID:    location,
		SKU:           sku,
		Quantity:      quantity,
		UnitCostCents: unitCost,
		LotCode:       lot,
		SerialNumbers: serials,
	})
	if err != nil {
		t.Fatalf("receive lot %s: %v", lot, err)
	}
	return p
}

func receiveLotWithExpiry(t *testing.T, svc *Service, tenant, location, sku, lot string, quantity, unitCost int64, expiresAt *time.Time) Position {
	t.Helper()
	p, _, err := svc.Receive(t.Context(), tenant, "test", "", ReceiveInput{
		LocationID:    location,
		SKU:           sku,
		Quantity:      quantity,
		UnitCostCents: unitCost,
		LotCode:       lot,
		ExpiresAt:     expiresAt,
	})
	if err != nil {
		t.Fatalf("receive lot %s: %v", lot, err)
	}
	return p
}

func TestLotSerialsFollowFIFOAndConfirm(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-lots"

	receiveLot(t, svc, tenant, "loc-1", "sku-1", "L1", 2, 100, []string{"S1", "S2"})
	receiveLot(t, svc, tenant, "loc-1", "sku-1", "L2", 1, 200, []string{"S3"})

	reservation := mustReserve(t, svc, tenant, "loc-1", "sku-1", 2)
	confirmed, _, err := svc.Confirm(t.Context(), tenant, "test", "", reservation.ID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if len(confirmed.Fulfillment) != 1 {
		t.Fatalf("fulfillment = %+v, want one layer", confirmed.Fulfillment)
	}
	use := confirmed.Fulfillment[0]
	if use.LotCode != "L1" || use.Quantity != 2 || len(use.Serials) != 2 {
		t.Fatalf("fulfillment = %+v, want L1 qty 2 with two serials", use)
	}
	if use.Serials[0] != "S1" || use.Serials[1] != "S2" {
		t.Fatalf("serials = %v, want S1,S2", use.Serials)
	}

	position, err := svc.GetPosition(t.Context(), tenant, "loc-1", "sku-1")
	if err != nil {
		t.Fatalf("position: %v", err)
	}
	if position.OnHand != 1 || position.Held != 0 || position.InventoryValueCents != 200 {
		t.Fatalf("position = %+v, want on_hand=1 value=200", position)
	}

	lots, err := svc.ListLots(t.Context(), tenant, "loc-1", "sku-1", 10)
	if err != nil {
		t.Fatalf("list lots: %v", err)
	}
	if len(lots) != 2 || lots[0].LotCode != "L1" || lots[0].Remaining != 0 || lots[1].Remaining != 1 {
		t.Fatalf("lots = %+v", lots)
	}

	serials, err := svc.ListSerials(t.Context(), tenant, "loc-1", "sku-1", "L1", "shipped", 10)
	if err != nil {
		t.Fatalf("list serials: %v", err)
	}
	if len(serials) != 2 {
		t.Fatalf("shipped serials = %+v", serials)
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestQualityHoldSkipsHeldLotAndBlocksAvailability(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-hold"

	receiveLot(t, svc, tenant, "loc-1", "sku-1", "L1", 2, 100, []string{"S1", "S2"})
	receiveLot(t, svc, tenant, "loc-1", "sku-1", "L2", 2, 200, []string{"S3", "S4"})

	lot, _, err := svc.HoldLot(t.Context(), tenant, "test", "", HoldLotInput{
		LocationID: "loc-1", SKU: "sku-1", LotCode: "L1", Hold: true,
	})
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if lot.Status != "hold" || lot.Remaining != 2 {
		t.Fatalf("lot = %+v", lot)
	}

	position, err := svc.GetPosition(t.Context(), tenant, "loc-1", "sku-1")
	if err != nil {
		t.Fatalf("position: %v", err)
	}
	if position.Held != 2 || position.Available != 2 {
		t.Fatalf("position = %+v, want held=2 available=2", position)
	}

	atp, err := svc.ATP(t.Context(), tenant, "loc-1", "sku-1")
	if err != nil {
		t.Fatalf("atp: %v", err)
	}
	if atp.Held != 2 || atp.Available != 2 {
		t.Fatalf("atp = %+v, want held=2 available=2", atp)
	}

	reservation := mustReserve(t, svc, tenant, "loc-1", "sku-1", 2)
	confirmed, _, err := svc.Confirm(t.Context(), tenant, "test", "", reservation.ID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if len(confirmed.Fulfillment) != 1 || confirmed.Fulfillment[0].LotCode != "L2" {
		t.Fatalf("fulfillment = %+v, want held L1 skipped and L2 consumed", confirmed.Fulfillment)
	}

	if _, _, err := svc.Adjust(t.Context(), tenant, "test", "", AdjustmentInput{
		LocationID: "loc-1", SKU: "sku-1", QuantityDelta: -1, Reason: "write-off",
	}); !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("adjustment against held stock error = %v, want ErrInsufficientStock", err)
	}

	if _, _, err := svc.HoldLot(t.Context(), tenant, "test", "", HoldLotInput{
		LocationID: "loc-1", SKU: "sku-1", LotCode: "L1", Hold: false,
	}); err != nil {
		t.Fatalf("release hold: %v", err)
	}
	position, err = svc.GetPosition(t.Context(), tenant, "loc-1", "sku-1")
	if err != nil {
		t.Fatalf("position after unhold: %v", err)
	}
	if position.Held != 0 || position.Available != 2 {
		t.Fatalf("position after unhold = %+v, want held=0 available=2", position)
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestSerialTransferMovesSerials(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-serial-transfer"

	receiveLot(t, svc, tenant, "loc-a", "sku-1", "L1", 2, 100, []string{"S1", "S2"})

	transfer, _, err := svc.CreateTransfer(t.Context(), tenant, "test", "", TransferInput{
		FromLocationID: "loc-a", ToLocationID: "loc-b", SKU: "sku-1", Quantity: 1,
	})
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	if transfer.Status != "in_transit" {
		t.Fatalf("transfer = %+v", transfer)
	}
	inTransit, err := svc.ListSerials(t.Context(), tenant, "loc-a", "sku-1", "L1", "in_transit", 10)
	if err != nil {
		t.Fatalf("in-transit serials: %v", err)
	}
	if len(inTransit) != 1 || inTransit[0].Serial != "S1" {
		t.Fatalf("in-transit serials = %+v", inTransit)
	}

	if _, _, err := svc.ReceiveTransfer(t.Context(), tenant, "test", "", transfer.ID); err != nil {
		t.Fatalf("receive transfer: %v", err)
	}
	destination, err := svc.ListSerials(t.Context(), tenant, "loc-b", "sku-1", "L1", "in_stock", 10)
	if err != nil {
		t.Fatalf("destination serials: %v", err)
	}
	if len(destination) != 1 || destination[0].Serial != "S1" {
		t.Fatalf("destination serials = %+v", destination)
	}
	source, err := svc.ListSerials(t.Context(), tenant, "loc-a", "sku-1", "L1", "in_stock", 10)
	if err != nil {
		t.Fatalf("source serials: %v", err)
	}
	if len(source) != 1 || source[0].Serial != "S2" {
		t.Fatalf("source serials = %+v", source)
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestHTTPLotHoldAndSerialAudit(t *testing.T) {
	server, _ := testHTTP(t)
	client := server.Client()
	tenant := "tenant-lot-http"

	resp := doRequest(t, client, http.MethodPost, server.URL+"/v1/receipts", tenant, "",
		`{"location_id":"loc-1","sku":"sku-1","quantity":2,"unit_cost_cents":100,"lot_code":"L1","serial_numbers":["S1","S2"]}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("receipt status = %d", resp.StatusCode)
	}

	resp = doRequest(t, client, http.MethodPost, server.URL+"/v1/lots/hold", tenant, "",
		`{"location_id":"loc-1","sku":"sku-1","lot_code":"L1","hold":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hold status = %d body=%s", resp.StatusCode, readAll(t, resp))
	}

	resp = doRequest(t, client, http.MethodGet, server.URL+"/v1/lots?location_id=loc-1&sku=sku-1", tenant, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lots status = %d", resp.StatusCode)
	}
	lots := decodeBody[struct {
		Lots []Lot `json:"lots"`
	}](t, resp)
	if len(lots.Lots) != 1 || lots.Lots[0].Status != "hold" || lots.Lots[0].Remaining != 2 {
		t.Fatalf("lots = %+v", lots.Lots)
	}

	resp = doRequest(t, client, http.MethodGet, server.URL+"/v1/serials?location_id=loc-1&sku=sku-1", tenant, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("serials status = %d", resp.StatusCode)
	}
	serials := decodeBody[struct {
		Serials []Serial `json:"serials"`
	}](t, resp)
	if len(serials.Serials) != 2 {
		t.Fatalf("serials = %+v", serials.Serials)
	}

	resp = doRequest(t, client, http.MethodPost, server.URL+"/v1/reservations", tenant, "",
		`{"location_id":"loc-1","sku":"sku-1","quantity":1}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("reservation against held lot status = %d, want 409", resp.StatusCode)
	}
}

func TestReceiptAndTransferIntoHeldLotStayHeld(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-held-destination"

	receiveLot(t, svc, tenant, "loc-a", "sku-1", "L1", 1, 100, nil)
	receiveLot(t, svc, tenant, "loc-b", "sku-1", "L1", 1, 100, nil)
	if _, _, err := svc.HoldLot(t.Context(), tenant, "test", "", HoldLotInput{
		LocationID: "loc-b", SKU: "sku-1", LotCode: "L1", Hold: true,
	}); err != nil {
		t.Fatalf("hold destination lot: %v", err)
	}

	receiveLot(t, svc, tenant, "loc-b", "sku-1", "L1", 2, 100, nil)
	position, err := svc.GetPosition(t.Context(), tenant, "loc-b", "sku-1")
	if err != nil {
		t.Fatalf("position after receipt into held lot: %v", err)
	}
	if position.Held != 3 || position.Available != 0 {
		t.Fatalf("position after receipt = %+v, want held=3 available=0", position)
	}

	transfer, _, err := svc.CreateTransfer(t.Context(), tenant, "test", "", TransferInput{
		FromLocationID: "loc-a", ToLocationID: "loc-b", SKU: "sku-1", Quantity: 1,
	})
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	if _, _, err := svc.ReceiveTransfer(t.Context(), tenant, "test", "", transfer.ID); err != nil {
		t.Fatalf("receive transfer: %v", err)
	}
	position, err = svc.GetPosition(t.Context(), tenant, "loc-b", "sku-1")
	if err != nil {
		t.Fatalf("position after transfer: %v", err)
	}
	if position.OnHand != 4 || position.Held != 4 || position.Available != 0 {
		t.Fatalf("position after transfer = %+v, want on_hand=4 held=4 available=0", position)
	}

	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestNegativeAdjustmentMarksSerialsAdjusted(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-serial-adjust"

	receiveLot(t, svc, tenant, "loc-1", "sku-1", "L1", 2, 100, []string{"S1", "S2"})
	if _, _, err := svc.Adjust(t.Context(), tenant, "test", "", AdjustmentInput{
		LocationID: "loc-1", SKU: "sku-1", QuantityDelta: -1, Reason: "damaged",
	}); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	serials, err := svc.ListSerials(t.Context(), tenant, "loc-1", "sku-1", "L1", "adjusted", 10)
	if err != nil {
		t.Fatalf("list adjusted serials: %v", err)
	}
	if len(serials) != 1 || serials[0].Serial != "S1" {
		t.Fatalf("adjusted serials = %+v, want S1", serials)
	}
	report, err := svc.Reconcile(t.Context(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.OK {
		t.Fatalf("reconciliation failed: %v", report.Issues)
	}
}

func TestLotExpiryMustMatchExistingLot(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-lot-expiry"
	first := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	second := first.Add(48 * time.Hour)

	receiveLotWithExpiry(t, svc, tenant, "loc-1", "sku-1", "L1", 1, 100, &first)
	_, _, err := svc.Receive(t.Context(), tenant, "test", "", ReceiveInput{
		LocationID: "loc-1", SKU: "sku-1", Quantity: 1, UnitCostCents: 100,
		LotCode: "L1", ExpiresAt: &second,
	})
	if err == nil {
		t.Fatal("expected expiry mismatch error")
	}
}

func TestReceiptRejectsInvalidSerialMetadata(t *testing.T) {
	svc, _ := testService(t)
	tenant := "tenant-serial-validation"

	_, _, err := svc.Receive(t.Context(), tenant, "test", "", ReceiveInput{
		LocationID: "loc-1", SKU: "sku-1", Quantity: 2, UnitCostCents: 100,
		LotCode: "L1", SerialNumbers: []string{"S1"},
	})
	if err == nil {
		t.Fatal("expected serial count validation error")
	}

	_, _, err = svc.Receive(t.Context(), tenant, "test", "", ReceiveInput{
		LocationID: "loc-1", SKU: "sku-1", Quantity: 2, UnitCostCents: 100,
		LotCode: "L1", SerialNumbers: []string{"S1", "S1"},
	})
	if err == nil {
		t.Fatal("expected duplicate serial validation error")
	}

	expires := time.Now().Add(24 * time.Hour)
	_, _, err = svc.Receive(t.Context(), tenant, "test", "", ReceiveInput{
		LocationID: "loc-1", SKU: "sku-1", Quantity: 1, UnitCostCents: 100,
		ExpiresAt: &expires,
	})
	if err == nil {
		t.Fatal("expected expiry-without-lot validation error")
	}
}
