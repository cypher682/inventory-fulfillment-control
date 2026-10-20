# Architecture

## Service shape (v1)

Modular monolith with a single write path per inventory partition:

```
cmd/inventory        HTTP API + lifecycle
internal/inventory   positions, reservation, allocation, transfers
internal/atp         available-to-promise queries
internal/ledger      inventory sub-ledger + cost layers
internal/outbox      transactional outbox + dispatcher
internal/store       persistence (Postgres)
```

## Consistency model

| Domain | Guarantee | Mechanism |
|--------|-----------|-----------|
| Position per (tenant, location, sku) | strong | row lock + conditional update |
| Reservation | strong | decrement available in the same transaction |
| Multi-location reservation | strong per location, all-or-nothing across locations | ordered locks + rollback, or reservation hierarchy |
| Transfer in-transit | causal | transfer state machine + compensating release |
| Sub-ledger | strong + balanced | posting in the fulfillment transaction |
| Downstream consumers | eventual | outbox |

## Invariants

1. `available = on_hand - reserved - quarantined`, never negative.
2. A reservation is `confirmed` only if its quantity was decremented.
3. Releasing a reservation restores exactly the reserved quantity once (idempotent by reservation id).
4. Every quantity change with financial meaning emits a balanced inventory transaction.
5. All commands carry idempotency keys.

## Hot-SKU strategy

MVP: one row per (tenant, location, sku) with `SELECT ... FOR UPDATE` and a conditional
`UPDATE ... WHERE available >= $qty` guard. This is correct and measurable before adding
partitioning or sharded counters. The load test is the gate for the next step.
