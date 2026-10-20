# MVP Scope & Limits

## Goal

Prove the core inventory invariant end to end before adding scale mechanics:

> A unit of stock can never be promised twice, every physical movement keeps the inventory sub-ledger balanced, and the current state can be checked against its own history.

## In scope

| Capability | Implementation |
|------------|----------------|
| Multi-tenant positions | `positions` keyed by `(tenant, location, sku)` |
| Receipts | append movement, add value at unit cost |
| Reservations | `SELECT ... FOR UPDATE` + conditional availability check |
| Release | restore reserved quantity |
| Confirm / ship | decrement on-hand + reserved, post COGS vs inventory |
| Transfers | source out, in-transit value, destination in |
| ATP | available = on-hand - reserved; incoming in-transit |
| Idempotency | `idempotency_keys`, request hash, stored response |
| Events | append-only `inventory_events` + `outbox` in the same transaction |
| Audit | reconciliation endpoint over movements, reservations, ledger |
| Authn/authz | intentionally out of scope; tenant header is trusted |

## Invariants

1. `available = on_hand - reserved >= 0` for every position.
2. A reservation succeeds only if the available quantity was sufficient at commit time.
3. Releasing or confirming a reservation changes `reserved` exactly once.
4. Every movement with financial impact posts debit and credit entries of equal value.
5. `on_hand` equals the sum of quantity movements for the position.
6. `inventory_value_cents` equals the `inventory` ledger balance for the position.
7. In-transit ledger balance equals the value of transfers still in transit.
8. A replayed idempotency key returns the original response and never re-applies effects.

## Evidence

| Test | Proves |
|------|--------|
| `TestConcurrentReservationsDoNotOversell` | 250 concurrent attempts against 100 units reserve exactly 100 |
| `TestConfirmPostsBalancedLedger` | COGS/inventory postings balance; position value matches ledger |
| `TestTransferMovesQuantityAndValue` | source/destination/in-transit quantities and values reconcile |
| `TestReceiveIsIdempotent` | replay does not duplicate movements or ledger rows; mismatched payload is rejected |
| `TestTenantIsolation` | reads from another tenant cannot see a position or reservation |
| `TestReleaseRestoresAvailability` | release restores available quantity and blocks a later confirm |
| `TestHTTPFlow` | full API path with tenant and idempotency headers |

`cmd/loadtest` repeats the oversell proof over HTTP and checks reconciliation at the end.

## Deliberate limits

- No reservation expiry job. `expires_at` is recorded and returned, but stock is held until release or confirm.
- Moving-value costing with integer division; long-tail rounding is absorbed into the last unit.
- One database, one write path. Hot-SKU partitioning and sharded counters are deferred until the load test demands them.
- The outbox is a local stub: events are logged and marked published, not delivered to a broker.
- No negative-stock backorders, lot tracking, serials, or quality holds in this MVP.

## Next milestones

1. Expiry sweeper with `SKIP LOCKED`, plus a test that expired reservations are not confirmable.
2. Audit query API over `inventory_events` and ledger entries.
3. Postgres load benchmark with p50/p99 reservation latency and contention metrics.
4. FIFO cost layers and per-layer valuation.
5. Authentication and signed internal service identity.
6. Partitioning strategy for hot `(tenant, location, sku)` rows.
