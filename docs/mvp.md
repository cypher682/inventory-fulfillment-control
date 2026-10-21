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
| Expiry | `SKIP LOCKED` sweeper releases expired reservations; confirm rejects them |
| Release | restore reserved quantity |
| Confirm / ship | decrement on-hand + reserved, post COGS vs inventory |
| Transfers | source out, in-transit value, destination in |
| Adjustments | signed quantity change with a required reason; cycle-count corrections |
| ATP | available = on-hand - reserved + expired-not-yet-swept; incoming in-transit |
| Idempotency | `idempotency_keys`, request hash, stored response |
| Events | append-only `inventory_events` + relay-ready outbox in the same transaction |
| Audit | paginated events, movements, and ledger queries per tenant |
| Reconciliation | snapshot-consistent check over movements, reservations, ledger, transfers |
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
9. An expired reservation cannot be confirmed; its reserved quantity is restored exactly once.
10. An adjustment cannot consume quantity that is reserved for an active order.

## Evidence

| Test | Proves |
|------|--------|
| `TestConcurrentReservationsDoNotOversell` | 250 concurrent attempts against 100 units reserve exactly 100 |
| `TestConfirmPostsBalancedLedger` | COGS/inventory postings balance; position value matches ledger |
| `TestTransferMovesQuantityAndValue` | source/destination/in-transit quantities and values reconcile |
| `TestReceiveIsIdempotent` | replay does not duplicate movements or ledger rows; mismatched payload is rejected |
| `TestExpireReservationsRestoresStock` | expired stock is released once and cannot be confirmed |
| `TestATPReportsExpiredReservations` | ATP ignores expired claims before the sweeper runs |
| `TestAdjustmentsPreserveValueAndLedger` | gains/losses balance, respect reservations, and replay safely |
| `TestAuditQueriesAreTenantScopedAndPaginated` | audit pages are ordered, filtered, and isolated per tenant |
| `TestOutboxRelayPublishesAndRetries` | relay marks success, records failures, and retries |
| `TestTenantIsolation` | reads from another tenant cannot see a position or reservation |
| `TestReleaseRestoresAvailability` | release restores available quantity and blocks a later confirm |
| `TestHTTPAdjustmentsAuditAndExpiry` | full API path for adjustments, audit, and expired confirm |

`cmd/loadtest` repeats the oversell proof over HTTP, reports p50/p95/p99 latency, and checks reconciliation at the end.

## Deliberate limits

- Moving-value costing with integer division; long-tail rounding is absorbed into the last unit.
- One database, one write path. Hot-SKU partitioning and sharded counters are deferred until the load test demands them.
- The outbox relay is broker-agnostic but ships with a log publisher; no broker adapter is included.
- No event replay/rebuild of positions from the log yet.
- No negative-stock backorders, lot tracking, serials, or quality holds in this MVP.

## Next milestones

1. FIFO cost layers and per-layer valuation.
2. Rebuild positions from the event log and verify against snapshots.
3. Broker-backed outbox publisher (Kafka/NATS) with consumer examples.
4. Authentication and signed internal service identity.
5. Partitioning strategy for hot `(tenant, location, sku)` rows.
