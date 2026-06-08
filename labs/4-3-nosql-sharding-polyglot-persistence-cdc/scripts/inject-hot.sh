#!/usr/bin/env bash
# Tell the fault-injector to skew $WEIGHT fraction of writes toward
# $ENTITY. The loadgen polls the injector every 200ms and applies the
# resulting skew on subsequent writes.
set -euo pipefail
ENTITY="${ENTITY:-tenant-A}"
WEIGHT="${WEIGHT:-0.35}"
PORT="${LAB_FAULT_INJECTOR_PORT:-19001}"
SLOT="${SLOT:-hot}"

curl -fsS -X PUT "http://localhost:${PORT}/faults/${SLOT}" \
  -H 'Content-Type: application/json' \
  -d "{\"mode\":\"hot\",\"entity\":\"${ENTITY}\",\"weight\":${WEIGHT}}" | jq .
