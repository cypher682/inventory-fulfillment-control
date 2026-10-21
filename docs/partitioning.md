# Hot-key partitioning strategy

Status: strategy and contention gate. No schema sharding is enabled yet.

The hot row is one `positions` row per `(tenant, location, sku)`. Reservations,
confirmations, releases, transfers, and adjustments all serialize on that row.
The current implementation keeps the row authoritative and uses a single
statement that reserves only when `on_hand - reserved >= quantity`:

```sql
UPDATE positions
SET reserved = reserved + $4, version = version + 1, updated_at = now()
WHERE tenant_id = $1 AND location_id = $2 AND sku = $3
  AND on_hand - reserved >= $4
RETURNING reserved;
```

If the update returns no row, the service distinguishes a missing position from
insufficient stock. This is still one row lock, but it avoids a separate
`SELECT ... FOR UPDATE` round trip and shortens the lock hold time.

## Measured baseline

`cmd/loadtest` now spreads attempts over `-skus` positions. The same total stock
and attempt count makes single-SKU contention comparable with a spread workload.
Observed on the development PostgreSQL instance, 400 units and 1,200 attempts:

| Workload | Workers | Throughput | p50 | p99 |
|----------|---------|------------|-----|-----|
| 1 SKU | 64 | 277 req/s | 99 ms | 916 ms |
| 16 SKUs | 64 | 801 req/s | 39 ms | 368 ms |
| 1 SKU | 128 | 221 req/s | 344 ms | 1.64 s |

The single-SKU path is correct but clearly queue-bound: more workers make p99
worse instead of increasing throughput. Sixteen independent SKUs deliver about
2.9x the throughput at the same worker count. These are synthetic numbers, not a
production SLA.

Reproduce with a running service:

```bash
go run ./cmd/loadtest -stock 400 -attempts 1200 -workers 64 -skus 1
go run ./cmd/loadtest -stock 400 -attempts 1200 -workers 64 -skus 16
```

## Options considered

| Option | Effect | Verdict |
|--------|--------|---------|
| PostgreSQL table partitioning by position key | Does not split one row; each key still has one row | Rejected for contention |
| Optimistic retry loop | Same row lock, more round trips under contention | Rejected |
| Per-SKU in-process queue | Coalesces one instance only; adds latency and splits correctness across instances | Rejected as primary design |
| Sharded `reserved` counters with capacity leases | Distributes reservation writes across multiple rows | Selected when the gate is exceeded |
| Shard `on_hand`, cost layers, and ledger | Splits the entire accounting model and FIFO ordering | Out of scope |

## Selected design: capacity-lease buckets

Only reservation capacity is sharded. `on_hand`, inventory value, FIFO cost
layers, movements, and ledger entries stay in the existing position-level
accounting model, so costing and reconciliation semantics do not change.

Each position gets `N` reservation buckets (start with 16):

```text
reservation_buckets(
  tenant_id, location_id, sku, bucket_id,
  capacity,        -- units leased from the position to this bucket
  reserved,         -- active reservations in this bucket
  PRIMARY KEY (tenant_id, location_id, sku, bucket_id),
  CHECK (reserved >= 0),
  CHECK (reserved <= capacity)
)
```

Invariants:

1. `SUM(capacity) <= positions.on_hand`. Unleased stock remains at the position.
2. `SUM(reserved)` across buckets equals the sum of active reservation quantities.
3. A bucket never reserves more than its capacity.
4. Receipts increase `on_hand` and the unleased pool; confirmations decrease both
   `on_hand` and the confirming bucket's capacity.
5. FIFO ordering remains global because cost layers are not sharded.

### Reservation path

1. Pick `preferred = hash(reservation_id) % N`.
2. Fast path: `UPDATE reservation_buckets SET reserved = reserved + q WHERE ...
   AND bucket_id = preferred AND capacity - reserved >= q RETURNING ...`.
3. If the preferred bucket lacks capacity, lease a batch (for example 64 units)
   from the position with an atomic update on `on_hand - leased >= batch`, then
   retry the bucket update. Leasing touches the position row only once per batch,
   not once per reservation.
4. If the reservation cannot fit in one bucket and the unleased pool is empty,
   fall back to locking buckets in ascending `bucket_id` order and splitting the
   reservation across their free capacity. Lock order is fixed to prevent
   deadlocks. The fallback records one `reservation_allocations` row per bucket.
5. If total free capacity is below `q`, return `ErrInsufficientStock`.

### Confirm and release

`reservation_allocations` identifies the buckets held by a reservation.
Release decrements `reserved` in each bucket. Confirm decrements both `reserved`
and `capacity`, then applies the existing global FIFO/on-hand/ledger transaction.
Confirmation still touches the position row because it changes `on_hand` and
value; that path is lower volume than reservation creation and remains the
accounting serialization point.

### Receipts, transfers, and adjustments

Receipts add to `on_hand` and the unleased pool. Transfer-in behaves like a
receipt; transfer-out and negative adjustments reduce bucket capacity and
`on_hand` under the existing position lock. Buckets are only a reservation
capacity cache; the position, movements, layers, and ledger remain authoritative
for quantity and value.

### Reconciliation

Extend reconciliation with:

- `SUM(bucket.capacity) <= positions.on_hand`
- `SUM(bucket.reserved) == SUM(active reservations)`
- no bucket with `reserved > capacity`
- every active reservation has allocations whose quantities sum to its quantity

### Migration and cutover

1. Add bucket and allocation tables; no behavior change.
2. Backfill buckets with zero capacity for existing positions.
3. Dual-write reservations to the existing `position.reserved` and buckets in
   shadow mode; compare reconciliation snapshots.
4. Move reads to bucket-derived availability, then enable the bucket fast path.
5. Keep the current single-row path as a feature-flag fallback until the shadow
   comparison is clean for a full reservation-expiry cycle.

## Gate for implementation

Implement and enable the bucket path when any of these holds for a sustained
window on production-like traffic:

- single-SKU p99 reservation latency above 250 ms at the target worker count;
- single-SKU throughput plateaus or drops as concurrency increases;
- PostgreSQL shows a material share of time in `Lock: transactionid` or
  `Lock: tuple` waits for the reservation path.

The synthetic benchmark already crosses the p99 gate for one SKU, so the bucket
design is approved. It is intentionally not enabled in this milestone because it
requires a schema migration, shadow comparison, and a new reconciliation proof;
the current atomic update remains the correct fallback.

## What this does not solve

- Confirmation of an extremely hot SKU still serializes on `positions` for
  `on_hand` and value.
- Cost layers and ledger postings remain position-scoped.
- Cross-instance admission control is not added; the database remains the
  correctness boundary.
