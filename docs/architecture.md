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
6. appends movements, ledger entries, an event, and an outbox row;
7. stores the idempotency response and commits.

A failure at any step rolls back all of it.

## Consistency model

| Domain | Guarantee | Mechanism |
|--------|-----------|-----------|
| Position per `(tenant, location, sku)` | strong | row lock + conditional update |
| Reservation | strong | available check and `reserved` increment in one transaction |
| Multi-location transfer | strong at source, causal in transit | source decrement + in-transit record; destination receives later |
| Reservation expiry | strong | sweeper locks rows with `SKIP LOCKED`, restores reserved, marks expired |
| Adjustments | strong | guarded position update; cannot consume reserved stock |
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

Values are integer cents. The inventory value of a position moves with stock: a ship or transfer of `q` from `on_hand` units worth `V` moves `V * q / on_hand` (all of `V` when `q == on_hand`).

## Hot-SKU strategy

One row per position, locked with `SELECT ... FOR UPDATE`, then updated under `reserved <= on_hand`. This is correct and measurable. The concurrency test and `cmd/loadtest` are the gate before introducing partitioning, sharded counters, or per-SKU queues.

## Invariants checked by reconciliation

1. `on_hand` = sum of quantity movements.
2. `reserved` = sum of active reservations.
3. Tenant ledger debits = credits.
4. Position value = `inventory` ledger balance.
5. In-transit ledger = value of in-transit transfers.

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
- FIFO layers, lot/serial tracking, quality holds.
- Partitioning for extreme hot-key contention.
