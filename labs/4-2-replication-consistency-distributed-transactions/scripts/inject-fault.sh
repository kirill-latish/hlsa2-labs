#!/usr/bin/env bash
# PUT a fault spec into the fault-injector for a service.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

SERVICE="${SERVICE:?SERVICE is required (payment|inventory|shipping)}"
MODE="${MODE:-latency}"
P99_MS="${P99_MS:-2000}"
ERROR_RATE="${ERROR_RATE:-0}"

BODY=$(jq -n --arg m "${MODE}" --arg p "${P99_MS}" --arg e "${ERROR_RATE}" \
  '{mode: $m, p99_ms: ($p|tonumber), error_rate: ($e|tonumber)}')

log_step "inject-fault service=${SERVICE} ${BODY}"
docker compose exec -T fault-injector wget -qO- --method=PUT --header='Content-Type: application/json' --body-data="${BODY}" "http://localhost:9000/faults/${SERVICE}"
echo
