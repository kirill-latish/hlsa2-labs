#!/usr/bin/env bash
# Print the Debezium connector + tasks state.
set -euo pipefail
LAB_DEBEZIUM_PORT="${LAB_DEBEZIUM_PORT:-18083}"
URL="http://localhost:${LAB_DEBEZIUM_PORT}"

echo "[lab43] connectors:"
curl -fsS "${URL}/connectors" | jq .

echo
echo "[lab43] lab43-postgres status:"
curl -fsS "${URL}/connectors/lab43-postgres/status" | jq . || true
