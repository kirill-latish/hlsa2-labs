#!/usr/bin/env bash
# Idempotent POST of the Debezium connector. Safe to re-run; on subsequent
# runs we DELETE-then-PUT to apply config changes without 409s.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOST="${DEBEZIUM_HOST:-localhost}"
PORT="${DEBEZIUM_PORT:-${LAB_DEBEZIUM_PORT:-18083}}"
NAME="lab43-postgres"
CFG="${LAB_ROOT}/debezium/connector-postgres.json"

base="http://${HOST}:${PORT}"

echo "[lab43] waiting for Debezium Connect at ${base} ..."
for i in $(seq 1 90); do
    if curl -fsS "${base}/" >/dev/null 2>&1; then
        break
    fi
    sleep 2
done

# Extract just the inline config (Connect's PUT /connectors/{name}/config
# accepts the inner config object, not the wrapping {name, config} envelope).
inner_cfg="$(jq '.config' "${CFG}")"

if curl -fsS "${base}/connectors/${NAME}" >/dev/null 2>&1; then
    echo "[lab43] connector ${NAME} exists; updating config via PUT"
    code=$(curl -s -o /tmp/lab43-connect.json -w '%{http_code}' \
        -X PUT "${base}/connectors/${NAME}/config" \
        -H 'Content-Type: application/json' \
        -d "${inner_cfg}")
else
    echo "[lab43] connector ${NAME} not present; creating via POST"
    code=$(curl -s -o /tmp/lab43-connect.json -w '%{http_code}' \
        -X POST "${base}/connectors" \
        -H 'Content-Type: application/json' \
        --data-binary "@${CFG}")
fi

if [[ "${code}" != "200" && "${code}" != "201" ]]; then
    echo "[lab43] connector registration failed (HTTP ${code}):"
    cat /tmp/lab43-connect.json
    exit 1
fi
echo "[lab43] connector registration HTTP ${code}"
cat /tmp/lab43-connect.json | jq -r '.name + " | tasks=" + ((.tasks // []) | length | tostring)' || true
