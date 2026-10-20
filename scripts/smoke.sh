#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if [ -x /usr/local/go/bin/go ]; then
  export PATH="/usr/local/go/bin:$PATH"
fi

go build -o /tmp/inventory-smoke ./cmd/inventory
/tmp/inventory-smoke >/tmp/inventory-smoke.log 2>&1 &
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
echo "=== load test ==="
go run ./cmd/loadtest -stock 50 -attempts 150 -workers 32
