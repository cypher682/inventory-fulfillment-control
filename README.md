# Inventory, Allocation & Fulfillment Control Plane

High-scale inventory control plane for multi-location retailers, distributors, and 3PLs: strong-consistent reservations, deterministic allocation, ATP/CTP promising, transfer orders, and a reconcilable inventory sub-ledger.

## Status

Scaffold. Inventory positions, reservations, allocation, transfers, and costing are not implemented yet.

| Stage | Scope | State |
|-------|-------|-------|
| 0 | Service skeleton, config, health, CI-ready build | done |
| 1 | Single-location position + reservation | next |
| 2 | State machine + inventory transactions | planned |
| 3 | Multi-location + transfer orders | planned |
| 4 | ATP / promising engine | planned |
| 5 | Event-sourced fulfillment pipeline | planned |
| 6 | Costing + inventory sub-ledger | planned |
| 7 | Load testing + observability | planned |
| 8 | Multi-tenant controls + audit queries | planned |

## Documents

- [Product overview](12-inventory-fulfillment-control.md)
- [PRD](12-d01-prd.md)
- [Architecture](docs/architecture.md)

## Quick start

```sh
go test ./...
go run ./cmd/inventory
curl localhost:8092/healthz
```

Configuration is environment-based; see `.env.example`.

## Layout

```
cmd/inventory      service entrypoint
internal/config    env config
internal/health    liveness/readiness
docs               architecture and decisions
deploy             local dependencies (Postgres)
migrations         schema migrations
```
