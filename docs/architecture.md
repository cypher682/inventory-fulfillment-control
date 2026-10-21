# Architecture

## Service shape

Modular monolith with a single write path per inventory position:

```
cmd/inventory        HTTP API + lifecycle + expiry sweeper
cmd/loadtest         HTTP concurrency and latency harness
internal/inventory   positions, reservations, transfers, adjustments, ATP, audit, ledger
internal/store       pgx pool + embedded migrations
internal/outbox      lease-based transactional outbox relay with pluggable publishers
```

## Data model

| Table | Purpose |
|-------|---------|
| `tenants`, `locations`, `items` | master data, auto-provisioned by receipts and transfers |
| `positions` | materialized current state per `(tenant, location, sku)` |
| `reservations` | active / confirmed / released / expired claims |
| `transfers` | in-transit quantity and value between locations |
| `adjustments` | signed cycle-count and shrinkage corrections with a reason |
| `cost_layers` | FIFO layers with remaining quantity and remaining value |
| `transfer_layers` | per-layer cost staging for in-transit transfers |
| `cost_consumptions` | per-layer trace of every FIFO consumption |
| `inventory_movements` | append-only quantity/value deltas per position |
| `ledger_entries` | double-entry postings in integer cents |
| `inventory_events` | append-only domain events with actor and payload |
| `outbox` | pending events plus attempt count, lease time, and last error |
| `idempotency_keys` | request hash + stored response per tenant/key |

## Write path

Every mutation:

1. begins a transaction;
2. claims the idempotency key (if supplied) or reads the stored response;
3. validates input;
4. locks the position or reservation row with `SELECT ... FOR UPDATE`;
5. applies the state change with a guarded `UPDATE`;
6. consumes or appends FIFO cost layers as needed;
7. appends movements, ledger entries, an event, and an outbox row;
8. stores the idempotency response and commits.

A failure at any step rolls back all of it.

## Consistency model

| Domain | Guarantee | Mechanism |
|--------|-----------|-----------|
| Position per `(tenant, location, sku)` | strong | row lock + conditional update |
| Reservation | strong | available check and `reserved` increment in one transaction |
| Multi-location transfer | strong at source, causal in transit | source decrement + in-transit record; destination receives later |
| Reservation expiry | strong | sweeper locks rows with `SKIP LOCKED`, restores reserved, marks expired |
| Adjustments | strong | guarded position update; cannot consume reserved stock |
| Costing | strong | FIFO layers consumed under the position lock, with a per-layer trace |
| Ledger | strong + balanced | debit/credit rows written together; reconciliation verifies tenant balance |
| Downstream consumers | eventual, at-least-once | outbox lease + retry with backoff |
| Idempotent retries | effectively once | unique `(tenant, key)` + stored response |

## Ledger accounts

| Event | Debit | Credit |
|-------|-------|--------|
| Receipt | `inventory` | `inventory_source` |
| Ship / confirm | `cogs` | `inventory` |
| Transfer out | `inventory_in_transit` | `inventory` |
| Transfer in | `inventory` | `inventory_in_transit` |
| Adjustment gain | `inventory` | `inventory_adjustment` |
| Adjustment loss | `inventory_adjustment` | `inventory` |

Values are integer cents. COGS, transfer value, and adjustment value come from the oldest remaining layers. A partial layer is consumed at `remaining_value * take / remaining_quantity` (floor); fully consuming a layer takes all of its remaining value, so rounding never creates or destroys cents. Transfers carry each consumed layer through `transfer_layers` and recreate it at the destination, preserving acquisition order.

## Costing

Each receipt and positive adjustment appends a layer with quantity, value, and `received_at`. Ships, transfers out, and negative adjustments lock the position and consume layers ordered by `(received_at, id)`. Each consumption is written to `cost_consumptions` with the layer, quantity, value, and source reference. Reservations remain quantity-level; FIFO allocation happens at confirm, when the actual cost is known.

A transfer consumes source layers and writes the same chunks to `transfer_layers`. Receiving the transfer recreates them at the destination with their original `received_at` and marks the staging rows settled. This preserves acquisition order across locations.

## Hot-SKU strategy

One row per position, locked with `SELECT ... FOR UPDATE`, then updated under `reserved <= on_hand`. This is correct and measurable. The concurrency test and `cmd/loadtest` are the gate before introducing partitioning, sharded counters, or per-SKU queues.

## Invariants checked by reconciliation

1. `on_hand` = sum of quantity movements = sum of remaining layer quantity.
2. `reserved` = sum of active reservations.
3. Tenant ledger debits = credits.
4. Position value = `inventory` ledger balance = sum of remaining layer value.
5. In-transit ledger = value of in-transit transfers = pending transfer-layer value.
6. Movement value deltas = layer consumptions and additions for the position.

## Background workers

- Expiry sweeper runs every `EXPIRY_INTERVAL` (default 10s), processes `EXPIRY_BATCH` rows per tick, and emits `reservation.expired`.
- Outbox relay claims a batch under a 30s lease, publishes each envelope, marks success, and records failures with exponential backoff. Delivery is at-least-once; the bundled publisher logs only.

## Failure handling

- External commands are retryable through idempotency keys.
- A crash before commit leaves no partial movement or ledger entry.
- A crash after commit is safe to retry; the stored response is returned.
- The outbox guarantees an event exists for every committed state change. A crash after claiming delays delivery until the lease expires; a crash after publishing but before marking can duplicate delivery.
- Reconciliation uses a repeatable-read snapshot so concurrent writes cannot produce false drift.

## Not implemented yet

- Event replay/rebuild of positions from the log.
- Broker-backed outbox publisher.
- Authentication and authorization.
- Lot/serial tracking, quality holds, and LIFO/specific identification.
- Partitioning for extreme hot-key contention.
