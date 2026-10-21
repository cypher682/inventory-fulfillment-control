# API Reference

Base path: `/v1`. All requests require `X-Tenant-ID`. Mutating requests may send `X-Idempotency-Key`; reusing a key with the same body returns the original response with `Idempotent-Replayed: true`. Reusing it with a different body returns `422`.

`X-Actor` is optional and recorded in events (default `api`).

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
| 400 | `validation` / `invalid_json` / `tenant_required` | bad input or missing tenant header |
| 404 | `not_found` | unknown position, reservation, or transfer in this tenant |
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

Creates the tenant/location/item on first use, adds on-hand quantity and inventory value, posts `Dr inventory / Cr inventory_source`, and returns the position.

### POST /v1/adjustments

```json
{"location_id":"wh-a","sku":"sku-1","quantity_delta":-2,"reason":"damaged in handling"}
```

```json
{"location_id":"wh-a","sku":"sku-1","quantity_delta":3,"unit_cost_cents":600,"reason":"cycle count surplus"}
```

Applies a signed correction. Positive deltas add on-hand at `unit_cost_cents`; negative deltas remove on-hand at moving value and cannot consume reserved stock. Posts `Dr inventory / Cr inventory_adjustment` for gains and `Dr inventory_adjustment / Cr inventory` for losses. Returns the adjustment and the updated position.

### POST /v1/reservations

```json
{"location_id":"wh-a","sku":"sku-1","quantity":4,"ttl_seconds":900}
```

`ttl_seconds` defaults to 900 and is capped at 604800. Returns a reservation with `status: "active"`. Expired reservations are released by a background sweeper; confirming one returns `409 reservation_expired`.

### POST /v1/reservations/{id}/confirm

Ships the reserved quantity: decrements on-hand and reserved, posts `Dr cogs / Cr inventory` at moving value, and marks the reservation `confirmed`. Repeating the call returns the confirmed reservation.

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
  "available": 6,
  "inventory_value_cents": 3000,
  "version": 3,
  "updated_at": "2026-10-20T12:00:00Z"
}
```

### GET /v1/atp?location=wh-a&sku=sku-1

Returns on-hand, reserved, expired-but-not-yet-swept reserved, available, and incoming in-transit quantity. Unknown positions return `found: false` with zero quantities.

### POST /v1/transfers

```json
{"from_location_id":"wh-a","to_location_id":"wh-b","sku":"sku-1","quantity":2}
```

Moves quantity and value out of the source into in-transit (`Dr inventory_in_transit / Cr inventory`). Requires enough available stock at the source.

### POST /v1/transfers/{id}/receive

Adds quantity and value to the destination and clears in-transit (`Dr inventory / Cr inventory_in_transit`). Repeating the call is safe.

### GET /v1/transfers/{id}

Returns the transfer.

### GET /v1/reconciliation

```json
{"ok": true, "issues": [], "checked_at": "2026-10-20T12:00:00Z"}
```

Checks:

- on-hand equals the sum of quantity movements per position;
- reserved equals the sum of active reservations per position;
- the ledger balances for the tenant;
- each position's value equals its `inventory` ledger balance;
- the in-transit ledger equals the value of in-transit transfers.

Returns `409` when `ok` is false.

### GET /v1/audit/events

Pages through the tenant's event log. Filters: `aggregate_type`, `aggregate_id`, `event_type`, `after`, `limit`.

```json
{"events":[{"id":42,"aggregate_type":"reservation","event_type":"reservation.confirmed","payload":{}}],"next_after":42,"has_more":true}
```

### GET /v1/audit/movements

Pages through inventory movements. Filters: `location_id`, `sku`, `movement_type`, `after`, `limit`.

### GET /v1/audit/ledger

Pages through ledger entries. Filters: `account`, `location_id`, `sku`, `ref_type`, `ref_id`, `after`, `limit`.

Audit pagination is ascending by numeric `id`. Pass the returned `next_after` as `after` for the next page. `limit` defaults to 50 and is capped at 200.

### GET /metrics

Prometheus text output: operation counters, HTTP request counts/errors, an HTTP latency histogram, and unpublished outbox gauges.

### GET /healthz, GET /readyz

Liveness; readiness pings the database.
