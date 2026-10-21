# MVP Scope & Limits

## Goal

Prove the core inventory invariant end to end before adding scale mechanics:

> A unit of stock can never be promised twice, every physical movement keeps the inventory sub-ledger balanced, and the current state can be checked against its own history.

## In scope

| Capability | Implementation |
|------------|----------------|
| Multi-tenant positions | `positions` keyed by `(tenant, location, sku)` |
| Receipts | append movement and a FIFO cost layer |
| Reservations | `SELECT ... FOR UPDATE` + conditional availability check |
| Expiry | `SKIP LOCKED` sweeper releases expired reservations; confirm rejects them |
| Release | restore reserved quantity |
| Confirm / ship | consume oldest layers, decrement on-hand + reserved, post COGS vs inventory |
| Transfers | consume source layers, stage them in transit, recreate at destination |
| Adjustments | signed quantity change with a required reason; negative deltas consume FIFO |
| ATP | available = on-hand - reserved + expired-not-yet-swept; incoming in-transit |
| Idempotency | `idempotency_keys`, request hash, stored response |
| Events | append-only `inventory_events` + outbox in the same transaction |
| Delivery | log, NATS JetStream with ack/dedupe, or HMAC-signed HTTP webhook |
| Audit | paginated events, movements, ledger, cost layers, and consumptions per tenant |
| Reconciliation | snapshot-consistent check over movements, layers, reservations, ledger, transfers |
| Authn | optional per-tenant API keys; scoped authz and user identity are out of scope |

## Invariants

1. `available = on_hand - reserved >= 0` for every position.
2. A reservation succeeds only if the available quantity was sufficient at commit time.
3. Releasing or confirming a reservation changes `reserved` exactly once.
4. Every movement with financial impact posts debit and credit entries of equal value.
5. `on_hand` equals the sum of quantity movements and the sum of remaining layer quantities.
6. `inventory_value_cents` equals the `inventory` ledger balance and the sum of remaining layer values.
7. In-transit ledger balance equals the value of transfers still in transit and their pending layer value.
8. A replayed idempotency key returns the original response and never re-applies effects.
9. An expired reservation cannot be confirmed; its reserved quantity is restored exactly once.
10. An adjustment cannot consume quantity that is reserved for an active order.
11. A ship or transfer consumes the oldest layers first; consuming a full layer takes its exact remaining value.
12. With auth enabled, a valid key resolves to exactly one tenant and cannot be widened by `X-Tenant-ID`.

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
| `TestFIFOConfirmConsumesOldestLayers` | COGS follows receipt order and leaves the correct remaining layer |
| `TestFIFOTransferPreservesLayerCosts` | transferred layers keep their cost and acquisition order at the destination |
| `TestFIFONegativeAdjustmentUsesOldestLayers` | write-offs consume FIFO and preserve the sub-ledger |
| `TestConcurrentConfirmationsConsumeFIFOExactly` | 100 concurrent confirms consume the layers exactly once and balance COGS |
| `TestReplayRebuildsPositionsFromMovementLog` | replayed quantity/value match positions and drift is detected |
| `TestAuthRequiresValidKey` | missing and invalid keys return 401 before reaching a handler |
| `TestAuthBindsTenantToKey` | API keys resolve to one tenant and conflicting tenant headers return 403 |
| `TestAuthLeavesHealthOpenAndCanBeDisabled` | probes stay open and auth can be disabled for local use |
| `TestNATSPublisherPersistsAndDeduplicates` | JetStream stores the event and redelivers one copy for a repeated outbox ID |
| `TestWebhookPublisherSignsAndDelivers` | webhook headers, body, and HMAC signature match the contract |
| `TestAuditQueriesAreTenantScopedAndPaginated` | audit pages are ordered, filtered, and isolated per tenant |
| `TestOutboxRelayPublishesAndRetries` | relay marks success, records failures, and retries |
| `TestTenantIsolation` | reads from another tenant cannot see a position or reservation |
| `TestReleaseRestoresAvailability` | release restores available quantity and blocks a later confirm |
| `TestHTTPAdjustmentsAuditAndExpiry` | full API path for adjustments, audit, and expired confirm |

`cmd/loadtest` repeats the oversell proof over HTTP, reports p50/p95/p99 latency, and checks reconciliation at the end. The `-skus` flag spreads the same stock and attempt count across positions to compare a hot key with independent keys.

## Deliberate limits

- FIFO costing with integer division; a partial layer floors, and a fully consumed layer takes its exact remaining value.
- Migration `0003` backfills one synthetic layer per existing position; pre-FIFO history did not preserve layer identity.
- One database, one write path. Reservations use a single conditional update; the hot-key benchmark, capacity-lease design, and enablement gate are in `docs/partitioning.md`. Sharded counters are not active yet.
- The outbox relay ships with log, NATS JetStream, and HMAC webhook publishers. NATS dedupes by outbox ID for one hour; webhooks rely on receiver idempotency.
- Replay rebuilds positions from movements, not the full event stream; cost layers and reservations are verified by reconciliation instead.
- API keys are static secrets with full tenant access; there is no rotation, scoped permission, or per-user identity.
- Outbox delivery is at-least-once. NATS dedupes by outbox ID for one hour; webhook receivers must dedupe themselves. There is no dead-letter stream or schema registry.
- No negative-stock backorders, lot tracking, serials, quality holds, or LIFO/specific identification in this MVP.

## Next milestones

1. Lot/serial tracking and quality holds layered on top of FIFO.
2. Bucket-based reservation partitioning for hot `(tenant, location, sku)` rows.
3. Key rotation, scoped permissions, and signed internal service identity.
4. Full projection rebuild from `inventory_events`.
5. Dead-letter stream, consumer lag monitoring, and a schema registry.
