# Inventory, Allocation & Fulfillment Control Plane

High-scale inventory control plane for multi-location retailers, distributors, and 3PLs: strong-consistent reservations, deterministic allocation, ATP/CTP promising, transfer orders, and a reconcilable inventory sub-ledger.

## Status

MVP implemented on PostgreSQL. Correctness is enforced in the database transaction path and covered by integration tests, including a concurrent oversell test.

| Stage | Scope | State |
|-------|-------|-------|
| 0 | Service skeleton, config, health, CI-ready build | done |
| 1 | Single-location position + reservation | done |
| 2 | State machine + inventory transactions | done |
| 3 | Multi-location + transfer orders | done |
| 4 | ATP / promising engine | done (single-hop, in-transit aware) |
| 5 | Event-sourced fulfillment pipeline | partial: append-only events + outbox, no replay/rebuild |
| 6 | Costing + inventory sub-ledger | done: moving value + double-entry postings |
| 7 | Load testing + observability | partial: concurrency test + load generator + Prometheus counters |
| 8 | Multi-tenant controls + audit queries | partial: tenant isolation, no audit query API |

## What works

- Receipts create positions and cost value.
- Reservations decrement available stock under a row lock; concurrent orders cannot oversell.
- Confirm ships stock, releases the reservation, computes COGS, and posts a balanced journal entry.
- Release restores availability.
- Transfers move quantity and value between locations through in-transit, with ledger postings on both sides.
- ATP reports on-hand, reserved, available, and incoming in-transit quantity.
- Idempotency keys make mutations safely retryable.
- Append-only inventory events and a transactional outbox are written in the same transaction.
- Reconciliation checks position vs movements, reservations, ledger balance, inventory value, and in-transit value.
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

The command seeds stock, fires concurrent reservations, and fails if more than the available stock is reserved or if reconciliation is not clean.

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
internal/outbox     outbox dispatcher (local stub)
docs                architecture, API, MVP scope
deploy              local Postgres
migrations          migration notes (actual SQL is embedded in internal/store)
```

## Known limits

- Single currency; all values are integer cents.
- Moving-value costing, not FIFO layers.
- Reservations carry an expiry timestamp but no expiry sweeper or automatic release yet.
- The outbox dispatcher logs events and marks them published; it is not wired to a broker.
- No authentication; tenancy is asserted by the `X-Tenant-ID` header and must be trusted at the edge.
- No partition/sharding for hot rows yet; correctness is proven before throughput optimization.
