# API Reference

Base path: `/v1`. All requests require a tenant. Mutating requests may send `X-Idempotency-Key`; reusing a key with the same body returns the original response with `Idempotent-Replayed: true`. Reusing it with a different body returns `422`.

`X-Actor` is optional and recorded in events (default `api`).

## Authentication

Auth is disabled by default. When the service runs with `AUTH_REQUIRED=true`, send a key from `API_KEYS` as `X-API-Key: <key>` or `Authorization: Bearer <key>`. The key selects the tenant, so `X-Tenant-ID` is optional but must match when present. `X-API-Key` takes precedence when both key headers are sent.

`/healthz`, `/readyz`, and `/metrics` do not require a key. Invalid or missing keys return `401` with `WWW-Authenticate: Bearer`; a key used with a different `X-Tenant-ID` returns `403`.

## Errors

```json
{
  "error": {
    "code": "insufficient_stock",
    "message": "insufficient available stock: available=3 requested=5"
  }
}
```

| Status | Code | Meaning |
|--------|------|---------|
| 400 | `validation` / `invalid_json` / `tenant_required` | bad input or missing tenant |
| 401 | `unauthorized` | missing or invalid API key when auth is required |
| 403 | `tenant_mismatch` | API key does not belong to the requested tenant |
| 404 | `not_found` | unknown position, reservation, transfer, or lot in this tenant |
| 409 | `insufficient_stock` | available quantity is too low |
| 409 | `invalid_state` | reservation is already released/confirmed |
| 409 | `reservation_expired` | reservation expired and can no longer be confirmed |
| 409 | `request_in_progress` | same idempotency key still executing |
| 422 | `idempotency_mismatch` | key reused with a different body |
| 500 | `internal` | unexpected failure |

## Endpoints

### POST /v1/receipts

```json
{"location_id":"wh-a","sku":"sku-1","quantity":10,"unit_cost_cents":500}
```

```json
{"location_id":"wh-a","sku":"sku-1","quantity":2,"unit_cost_cents":500,"lot_code":"LOT-2026-10","expires_at":"2027-10-01T00:00:00Z","serial_numbers":["SN-1","SN-2"]}
```

Creates the tenant/location/item on first use, adds on-hand quantity and inventory value, appends a FIFO cost layer, posts `Dr inventory / Cr inventory_source`, and returns the position.

`lot_code` is optional. When present, the receipt is assigned to that lot and the lot is created or reused at the location. `expires_at` requires a lot. `serial_numbers` requires a lot and must contain exactly `quantity` unique values. Serial-tracked lots move their serials through confirmations, transfers, and negative adjustments.

### POST /v1/adjustments

```json
{"location_id":"wh-a","sku":"sku-1","quantity_delta":-2,"reason":"damaged in handling"}
```

```json
{"location_id":"wh-a","sku":"sku-1","quantity_delta":3,"unit_cost_cents":600,"reason":"cycle count surplus"}
```

Applies a signed correction. Positive deltas add on-hand and a new untracked FIFO layer at `unit_cost_cents`; negative deltas consume the oldest non-held layers, cannot consume reserved stock, and mark consumed serials `adjusted`. Posts `Dr inventory / Cr inventory_adjustment` for gains and `Dr inventory_adjustment / Cr inventory` for losses. Returns the adjustment and the updated position.

### POST /v1/reservations

```json
{"location_id":"wh-a","sku":"sku-1","quantity":4,"ttl_seconds":900}
```

`ttl_seconds` defaults to 900 and is capped at 604800. Returns a reservation with `status: "active"`. Expired reservations are released by a background sweeper; confirming one returns `409 reservation_expired`.

### POST /v1/reservations/{id}/confirm

Ships the reserved quantity: consumes the oldest non-held cost layers, decrements on-hand and reserved, posts `Dr cogs / Cr inventory` at the consumed layer value, marks serials `shipped`, and marks the reservation `confirmed`. The first response includes a `fulfillment` array with the lot, quantity, value, and serials taken from each layer. Repeating the call returns the confirmed reservation.

### POST /v1/reservations/{id}/release

Restores the reserved quantity and marks the reservation `released`. Repeating the call is safe.

### GET /v1/reservations/{id}

Returns the reservation.

### GET /v1/positions/{location}/{sku}

```json
{
  "location_id": "wh-a",
  "sku": "sku-1",
  "on_hand": 6,
  "reserved": 0,
  "held": 0,
  "available": 6,
  "inventory_value_cents": 3000,
  "version": 3,
  "updated_at": "2026-10-20T12:00:00Z"
}
```

### GET /v1/atp?location=wh-a&sku=sku-1

Returns on-hand, reserved, held, expired-but-not-yet-swept reserved, available, and incoming in-transit quantity. Quality-held lots are excluded from available. Unknown positions return `found: false` with zero quantities.

### POST /v1/transfers

```json
{"from_location_id":"wh-a","to_location_id":"wh-b","sku":"sku-1","quantity":2}
```

Moves quantity and value out of the source into in-transit (`Dr inventory_in_transit / Cr inventory`), consuming source FIFO layers and staging their cost. Lot codes travel with the staged layers, serials become `in_transit`, and held lots are skipped. Requires enough available stock at the source.

### POST /v1/transfers/{id}/receive

Adds quantity and value to the destination, recreates the staged FIFO layers with their lot codes, moves serials to `in_stock` at the destination, and clears in-transit (`Dr inventory / Cr inventory_in_transit`). Repeating the call is safe.

### GET /v1/transfers/{id}

Returns the transfer.

### POST /v1/lots/hold

```json
{"location_id":"wh-a","sku":"sku-1","lot_code":"LOT-2026-10","hold":true}
```

Places or releases a quality hold on a lot. Held quantity stays on hand but is removed from available and skipped by FIFO consumption, so a reservation cannot buy it. Holding fails with `409 insufficient_stock` if the lot quantity exceeds unreserved stock.

### GET /v1/lots?location_id=wh-a&sku=sku-1

Returns lot status, expiry, and remaining quantity. Filters are optional; `limit` defaults to 50 and is capped at 200.

### GET /v1/serials?location_id=wh-a&sku=sku-1&lot_code=LOT-2026-10&status=in_stock

Returns serial numbers with location, lot, layer, and status (`in_stock`, `in_transit`, `shipped`, `adjusted`). All filters are optional.

### GET /v1/reconciliation

```json
{"ok": true, "issues": [], "checked_at": "2026-10-20T12:00:00Z"}
```

Checks:

- on-hand equals the sum of quantity movements and remaining cost-layer quantity per position;
- reserved equals the sum of active reservations per position;
- held equals the remaining quantity in held lots per position;
- each serial-tracked layer's remaining quantity equals its in-stock serial count;
- the ledger balances for the tenant;
- each position's value equals its `inventory` ledger balance and remaining cost-layer value;
- the in-transit ledger equals the value of in-transit transfers and their pending layers.

Returns `409` when `ok` is false.

### GET /v1/audit/replay

Rebuilds each position from the tenant's movement log and compares it with the stored position. Read-only; uses a repeatable-read snapshot.

```json
{"ok":true,"tenant":"demo","positions":[{"location_id":"wh-a","sku":"sku-1","movement_count":5,"replayed_on_hand":4,"replayed_value_cents":400,"recorded_on_hand":4,"recorded_value_cents":400,"has_position":true}],"issues":[],"movement_count":5,"checked_at":"2026-10-21T12:00:00Z"}
```

Drift, movements without a position, and positions without movements appear in `issues`. Returns `409` when `ok` is false.

### GET /v1/audit/events

Pages through the tenant's event log. Filters: `aggregate_type`, `aggregate_id`, `event_type`, `after`, `limit`.

```json
{"events":[{"id":42,"aggregate_type":"reservation","event_type":"reservation.confirmed","payload":{}}],"next_after":42,"has_more":true}
```

### GET /v1/audit/movements

Pages through inventory movements. Filters: `location_id`, `sku`, `movement_type`, `after`, `limit`.

### GET /v1/audit/ledger

Pages through ledger entries. Filters: `account`, `location_id`, `sku`, `ref_type`, `ref_id`, `after`, `limit`.

### GET /v1/audit/cost-layers

Pages through FIFO layers. Filters: `location_id`, `sku`, `include_empty`, `after`, `limit`. Fully consumed layers are hidden unless `include_empty=true`.

```json
{"layers":[{"id":7,"location_id":"wh-a","sku":"sku-1","original_quantity":10,"remaining_quantity":6,"original_value_cents":5000,"remaining_value_cents":3000,"received_at":"2026-10-20T12:00:00Z","source_type":"receipt","source_id":"...","lot_code":"LOT-2026-10"}],"next_after":7,"has_more":false}
```

### GET /v1/audit/cost-consumptions

Pages through per-layer FIFO consumption traces. Filters: `layer_id`, `location_id`, `sku`, `ref_type`, `ref_id`, `after`, `limit`.

Audit pagination is ascending by numeric `id`. Pass the returned `next_after` as `after` for the next page. `limit` defaults to 50 and is capped at 200.

### GET /metrics

Prometheus text output: operation counters, HTTP request counts/errors, an HTTP latency histogram, and unpublished outbox gauges.

### GET /healthz, GET /readyz

Liveness; readiness pings the database.
