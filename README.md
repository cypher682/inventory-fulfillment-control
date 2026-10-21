# Inventory, Allocation & Fulfillment Control Plane

High-scale inventory control plane for multi-location retailers, distributors, and 3PLs: strong-consistent reservations, deterministic allocation, ATP/CTP promising, transfer orders, and a reconcilable inventory sub-ledger.

## Status

v0.7 implemented on PostgreSQL, with optional NATS JetStream delivery. Correctness is enforced in the database transaction path and covered by integration tests, including concurrent oversell, expiry, and FIFO allocation tests.

| Stage | Scope | State |
|-------|-------|-------|
| 0 | Service skeleton, config, health, CI-ready build | done |
| 1 | Single-location position + reservation | done |
| 2 | State machine + inventory transactions, adjustments, expiry | done |
| 3 | Multi-location + transfer orders | done |
| 4 | ATP / promising engine | done (single-hop, in-transit and expiry aware) |
| 5 | Event-sourced fulfillment pipeline | done: append-only events, durable outbox delivery, replay verifier |
| 6 | Costing + inventory sub-ledger | done: FIFO cost layers + double-entry postings |
| 7 | Load testing + observability | done: concurrency tests, load harness with percentiles, Prometheus counters/histograms |
| 8 | Multi-tenant controls + audit queries | done: tenant isolation, optional API keys, paginated audit APIs |
| 9 | Hot-key contention strategy | done: atomic reserve fast path, multi-SKU benchmark, capacity-lease design |

## What works

- Receipts create positions and append FIFO cost layers.
- Reservations decrement available stock with one conditional `UPDATE`; concurrent orders cannot oversell.
- Confirm ships stock, consumes the oldest cost layers, releases the reservation, and posts a balanced journal entry.
- Release restores availability. Expired reservations are released by a `SKIP LOCKED` sweeper, and ATP reports them as available immediately.
- Transfers move quantity and value between locations through in-transit, preserving the consumed layers for destination FIFO.
- ATP reports on-hand, reserved, expired-reserved, available, and incoming in-transit quantity.
- Adjustments and cycle-count corrections post to `inventory_adjustment`, cannot consume reserved stock, and keep the sub-ledger balanced.
- Idempotency keys make mutations safely retryable.
- Append-only inventory events and a transactional outbox are written in the same transaction; the relay supports leases, retries, and backoff.
- The relay can deliver to NATS JetStream (durable, acked, deduplicated by outbox ID), an HMAC-signed HTTP webhook, or the log.
- Reconciliation runs on a repeatable-read snapshot and checks position vs movements, cost layers, reservations, ledger balance, inventory value, and in-transit value.
- The replay verifier rebuilds position quantity and value from the append-only movement log and reports drift; it is available as an API and as `cmd/replay`.
- Audit APIs page through events, movements, ledger entries, cost layers, and layer consumptions per tenant.
- Multi-tenant isolation is enforced in every query.
- Optional per-tenant API keys bind each request to one tenant; health and metrics stay open for probes.

## Documents

- [Architecture](docs/architecture.md)
- [API reference](docs/api.md)
- [Hot-key partitioning strategy](docs/partitioning.md)
- [MVP scope and limits](docs/mvp.md)

## Quick start

```sh
docker compose -f deploy/docker-compose.yml up -d
go run ./cmd/inventory
```

The service starts on `:8092` and runs migrations on startup. `DATABASE_URL` defaults to the compose database.

## Authentication

Authentication is off by default for the local demo. To require per-tenant keys:

```sh
AUTH_REQUIRED=true API_KEYS="demo:0123456789abcdef,acme:fedcba9876543210" go run ./cmd/inventory
```

Send the key as `X-API-Key` or `Authorization: Bearer <key>`. The key selects the tenant; a conflicting `X-Tenant-ID` returns `403`. Keys must be at least 16 characters and must not contain `,` or `:`.

```sh
curl localhost:8092/healthz
./scripts/demo.sh
```

## Outbox delivery

`OUTBOX_PUBLISHER` selects the transport:

- `log` (default): log the event envelope.
- `nats`: publish to JetStream with `NATS_URL` (default `nats://localhost:4222`) and `NATS_STREAM` (default `inventory`). The service creates a durable stream on `inventory.>`; set `NATS_SUBJECT_PREFIX` (must end with `.`) to deploy under a prefix.
- `webhook`: POST to `WEBHOOK_URL` with `X-Inventory-Signature: t=<unix>,v1=<hex HMAC-SHA256>` over `<unix>.<body>` using `WEBHOOK_SECRET` (at least 16 characters).

The compose stack includes NATS on `:4222`. A minimal consumer is included:

```sh
docker compose -f deploy/docker-compose.yml up -d
OUTBOX_PUBLISHER=nats go run ./cmd/inventory
go run ./cmd/natsconsumer
```

The NATS publisher waits for a broker ack and uses the outbox ID as the JetStream message ID, so a retry after a lost publish is deduplicated. Webhook delivery is at-least-once; consumers should deduplicate by `X-Outbox-ID`.

## Load test

Start the service, then:

```sh
go run ./cmd/loadtest -stock 100 -attempts 500 -workers 64
go run ./cmd/loadtest -stock 400 -attempts 1200 -workers 64 -skus 16
```

The command seeds stock, fires concurrent reservations, prints p50/p95/p99 latency, and fails if more than the available stock is reserved or if reconciliation is not clean. `-skus` spreads the same total stock and attempts over multiple positions, which makes single-SKU contention comparable with a spread workload.

## Replay

```sh
go run ./cmd/replay -tenant demo
```

The command rebuilds positions from the movement log, prints the report as JSON, and exits non-zero on drift. The API exposes the same check at `GET /v1/audit/replay`.

## Tests

```sh
make test-integration   # starts Postgres + NATS, runs unit + integration tests
make test-race          # race detector (integration tests skip without TEST_DATABASE_URL)
```

The NATS test also skips unless `TEST_NATS_URL` is set; `make test-integration` sets it.

## Layout

```
cmd/inventory       service entrypoint
cmd/loadtest        HTTP concurrency / oversell harness
cmd/replay          rebuild positions from the movement log and report drift
cmd/natsconsumer    minimal JetStream consumer example
internal/config     env config
internal/health     liveness/readiness
internal/inventory  domain, HTTP API, ledger, reconciliation
internal/store      connection + embedded migrations
internal/outbox     outbox relay with leases, retries, NATS/webhook publishers
docs                architecture, API, MVP scope, hot-key strategy
deploy              local Postgres and NATS
migrations          migration notes (actual SQL is embedded in internal/store)
```

## Known limits

- Single currency; all values are integer cents.
- FIFO costing with per-layer remaining quantity and value; rounding is absorbed by the layer being fully consumed.
- Reservation expiry is swept every 10 seconds by default (`EXPIRY_INTERVAL`); ATP accounts for expired claims before the sweep runs.
- NATS delivery is at-least-once with a one-hour dedupe window; webhook delivery is at-least-once and relies on the receiver for idempotency. There is no dead-letter stream or schema registry.
- API keys are static per-tenant secrets from `API_KEYS`; there is no rotation, scopes, or user identity. When auth is disabled, tenancy is asserted by `X-Tenant-ID` and must be trusted at the edge.
- Replay verifies positions from movements; it does not rebuild cost layers or reservations from the event stream.
- Hot rows still serialize on one position. `docs/partitioning.md` records the measured contention, the capacity-lease bucket design, and the gate for enabling it; sharded counters are not active yet.
