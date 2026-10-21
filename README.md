# Inventory, Allocation & Fulfillment Control Plane

High-scale inventory control plane for multi-location retailers, distributors, and 3PLs: strong-consistent reservations, deterministic allocation, ATP/CTP promising, transfer orders, and a reconcilable inventory sub-ledger.

## Status

v0.2 implemented on PostgreSQL. Correctness is enforced in the database transaction path and covered by integration tests, including concurrent oversell and expiry tests.

| Stage | Scope | State |
|-------|-------|-------|
| 0 | Service skeleton, config, health, CI-ready build | done |
| 1 | Single-location position + reservation | done |
| 2 | State machine + inventory transactions, adjustments, expiry | done |
| 3 | Multi-location + transfer orders | done |
| 4 | ATP / promising engine | done (single-hop, in-transit and expiry aware) |
| 5 | Event-sourced fulfillment pipeline | partial: append-only events + relay-ready outbox, no replay/rebuild |
| 6 | Costing + inventory sub-ledger | done: FIFO cost layers + double-entry postings |
| 7 | Load testing + observability | done: concurrency tests, load harness with percentiles, Prometheus counters/histograms |
| 8 | Multi-tenant controls + audit queries | done: tenant isolation and paginated audit APIs |

## What works

- Receipts create positions and append FIFO cost layers.
- Reservations decrement available stock under a row lock; concurrent orders cannot oversell.
- Confirm ships stock, consumes the oldest cost layers, releases the reservation, and posts a balanced journal entry.
- Release restores availability. Expired reservations are released by a `SKIP LOCKED` sweeper, and ATP reports them as available immediately.
- Transfers move quantity and value between locations through in-transit, preserving the consumed layers for destination FIFO.
- ATP reports on-hand, reserved, expired-reserved, available, and incoming in-transit quantity.
- Adjustments and cycle-count corrections post to `inventory_adjustment`, cannot consume reserved stock, and keep the sub-ledger balanced.
- Idempotency keys make mutations safely retryable.
- Append-only inventory events and a transactional outbox are written in the same transaction; the relay supports leases, retries, and backoff.
- Reconciliation runs on a repeatable-read snapshot and checks position vs movements, cost layers, reservations, ledger balance, inventory value, and in-transit value.
- Audit APIs page through events, movements, ledger entries, cost layers, and layer consumptions per tenant.
- Multi-tenant isolation is enforced in every query.

## Documents

- [Architecture](docs/architecture.md)
- [API reference](docs/api.md)
- [MVP scope and limits](docs/mvp.md)

## Quick start

```sh
docker compose -f deploy/docker-compose.yml up -d
go run ./cmd/inventory
```

The service starts on `:8092` and runs migrations on startup. `DATABASE_URL` defaults to the compose database.

```sh
curl localhost:8092/healthz
./scripts/demo.sh
```

## Load test

Start the service, then:

```sh
go run ./cmd/loadtest -stock 100 -attempts 500 -workers 64
```

The command seeds stock, fires concurrent reservations, prints p50/p95/p99 latency, and fails if more than the available stock is reserved or if reconciliation is not clean.

## Tests

```sh
make test-integration   # starts Postgres, runs unit + integration tests
make test-race          # race detector (integration tests skip without TEST_DATABASE_URL)
```

## Layout

```
cmd/inventory       service entrypoint
cmd/loadtest        HTTP concurrency / oversell harness
internal/config     env config
internal/health     liveness/readiness
internal/inventory  domain, HTTP API, ledger, reconciliation
internal/store      connection + embedded migrations
internal/outbox      outbox relay with leases, retries, and a pluggable publisher
docs                architecture, API, MVP scope
deploy              local Postgres
migrations          migration notes (actual SQL is embedded in internal/store)
```

## Known limits

- Single currency; all values are integer cents.
- FIFO costing with per-layer remaining quantity and value; rounding is absorbed by the layer being fully consumed.
- Reservation expiry is swept every 10 seconds by default (`EXPIRY_INTERVAL`); ATP accounts for expired claims before the sweep runs.
- The outbox relay ships events through a pluggable publisher; the bundled publisher only logs. A broker adapter is not included.
- No authentication; tenancy is asserted by the `X-Tenant-ID` header and must be trusted at the edge.
- No event replay/rebuild of positions from the log yet.
- No partition/sharding for hot rows yet; correctness is proven before throughput optimization.
