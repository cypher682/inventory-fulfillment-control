#!/usr/bin/env bash
set -euo pipefail

BASE="${BASE_URL:-http://localhost:8092}"
TENANT="${TENANT:-demo-$(date +%s)}"
LOC_A="wh-a"
LOC_B="wh-b"
SKU="sku-demo"

pretty() {
  if command -v python3 >/dev/null 2>&1; then
    python3 -m json.tool
  else
    cat
  fi
}

post() {
  local path="$1" body="$2" key="${3:-}"
  if [ -n "$key" ]; then
    curl -fsS -X POST "$BASE$path" \
      -H "X-Tenant-ID: $TENANT" \
      -H "X-Actor: demo" \
      -H "X-Idempotency-Key: $key" \
      -H "Content-Type: application/json" \
      -d "$body"
  else
    curl -fsS -X POST "$BASE$path" \
      -H "X-Tenant-ID: $TENANT" \
      -H "X-Actor: demo" \
      -H "Content-Type: application/json" \
      -d "$body"
  fi
}

get() {
  curl -fsS "$BASE$1" -H "X-Tenant-ID: $TENANT"
}

id_of() {
  python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])'
}

echo "tenant: $TENANT"
echo
echo "1. health"
get "/healthz" | pretty

echo
echo "2. receive 10 units into $LOC_A at 500 cents"
post "/v1/receipts" "{\"location_id\":\"$LOC_A\",\"sku\":\"$SKU\",\"quantity\":10,\"unit_cost_cents\":500}" "receipt-1" | pretty

echo
echo "3. reserve 4 units"
RESERVATION=$(post "/v1/reservations" "{\"location_id\":\"$LOC_A\",\"sku\":\"$SKU\",\"quantity\":4}" "reserve-1")
echo "$RESERVATION" | pretty
RESERVATION_ID=$(printf '%s' "$RESERVATION" | id_of)

echo
echo "4. confirm reservation (ship + COGS posting)"
post "/v1/reservations/$RESERVATION_ID/confirm" "" "confirm-1" | pretty

echo
echo "5. reserve 3 units, then release them"
RELEASED=$(post "/v1/reservations" "{\"location_id\":\"$LOC_A\",\"sku\":\"$SKU\",\"quantity\":3}" "reserve-2")
RELEASED_ID=$(printf '%s' "$RELEASED" | id_of)
post "/v1/reservations/$RELEASED_ID/release" "" "release-1" | pretty

echo
echo "6. transfer 2 units from $LOC_A to $LOC_B"
TRANSFER=$(post "/v1/transfers" "{\"from_location_id\":\"$LOC_A\",\"to_location_id\":\"$LOC_B\",\"sku\":\"$SKU\",\"quantity\":2}" "transfer-1")
echo "$TRANSFER" | pretty
TRANSFER_ID=$(printf '%s' "$TRANSFER" | id_of)

echo
echo "7. ATP at $LOC_B while stock is in transit"
get "/v1/atp?location=$LOC_B&sku=$SKU" | pretty

echo
echo "8. receive the transfer"
post "/v1/transfers/$TRANSFER_ID/receive" "" "receive-transfer-1" | pretty

echo
echo "9. positions"
get "/v1/positions/$LOC_A/$SKU" | pretty
get "/v1/positions/$LOC_B/$SKU" | pretty

echo
echo "10. oversell attempt (expected 409)"
STATUS=$(curl -s -o /tmp/inventory-demo-error.json -w '%{http_code}' -X POST "$BASE/v1/reservations" \
  -H "X-Tenant-ID: $TENANT" \
  -H "Content-Type: application/json" \
  -d "{\"location_id\":\"$LOC_A\",\"sku\":\"$SKU\",\"quantity\":999}")
echo "status: $STATUS"
pretty < /tmp/inventory-demo-error.json
rm -f /tmp/inventory-demo-error.json

echo
echo "11. reconciliation"
get "/v1/reconciliation" | pretty

echo
echo "done: tenant $TENANT"
