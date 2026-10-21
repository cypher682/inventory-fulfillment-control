#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if [ -x /usr/local/go/bin/go ]; then
  export PATH="/usr/local/go/bin:$PATH"
fi

go build -o /tmp/inventory-smoke ./cmd/inventory
EXPIRY_INTERVAL="${EXPIRY_INTERVAL:-2s}" OUTBOX_INTERVAL="${OUTBOX_INTERVAL:-500ms}" /tmp/inventory-smoke >/tmp/inventory-smoke.log 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true' EXIT

for _ in $(seq 1 50); do
  if curl -fsS http://localhost:8092/healthz >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

BASE_URL="http://localhost:8092" ./scripts/demo.sh

echo
echo "=== expiry ==="
EXPIRY_TENANT="expiry-$(date +%s)"
curl -fsS -X POST "http://localhost:8092/v1/receipts" \
  -H "X-Tenant-ID: $EXPIRY_TENANT" \
  -H "Content-Type: application/json" \
  -d '{"location_id":"expiry-loc","sku":"expiry-sku","quantity":1,"unit_cost_cents":100}' >/dev/null
RESPONSE=$(curl -fsS -X POST "http://localhost:8092/v1/reservations" \
  -H "X-Tenant-ID: $EXPIRY_TENANT" \
  -H "Content-Type: application/json" \
  -d '{"location_id":"expiry-loc","sku":"expiry-sku","quantity":1,"ttl_seconds":1}')
RESERVATION_ID=$(printf '%s' "$RESPONSE" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
sleep 4
STATUS=$(curl -fsS "http://localhost:8092/v1/reservations/$RESERVATION_ID" -H "X-Tenant-ID: $EXPIRY_TENANT" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')
echo "reservation status: $STATUS"
[ "$STATUS" = "expired" ] || { echo "expected expired reservation"; exit 1; }

echo
echo "=== load test ==="
go run ./cmd/loadtest -stock 50 -attempts 150 -workers 32
