#!/usr/bin/env bash
# inject-fault - POST a fault spec to the fault-injector for one dep.
#
# Usage:
#   DEP=recommendations MODE=down                   bash scripts/inject-fault.sh
#   DEP=recommendations MODE=latency P99_MS=400     bash scripts/inject-fault.sh
#   DEP=reviews         MODE=errors  ERROR_RATE=0.1 bash scripts/inject-fault.sh
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"

if [[ -f .env ]]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi

DEP="${DEP:?DEP=price|cart|recommendations|reviews|recently-viewed is required}"
MODE="${MODE:?MODE=down|latency|errors is required}"
P99_MS="${P99_MS:-0}"
ERROR_RATE="${ERROR_RATE:-0}"

case "${DEP}" in
  price|cart|recommendations|reviews|recently-viewed) ;;
  *)
    echo "ERROR: DEP must be one of price|cart|recommendations|reviews|recently-viewed" >&2
    exit 2
    ;;
esac
case "${MODE}" in
  down|latency|errors) ;;
  *)
    echo "ERROR: MODE must be down|latency|errors" >&2
    exit 2
    ;;
esac

URL="http://localhost:${LAB_FAULT_INJECTOR_PORT:-9000}/faults/${DEP}"
PAYLOAD=$(printf '{"mode":"%s","p99_ms":%s,"error_rate":%s}' "${MODE}" "${P99_MS}" "${ERROR_RATE}")
echo "[inject-fault] dep=${DEP} ${PAYLOAD}"
curl -fsS -X POST -H 'content-type: application/json' -d "${PAYLOAD}" "${URL}"
echo

# Persist the active fault in perf/results so analyze-blast-radius.py
# and compare.sh can validate identical-fault before/after.
mkdir -p perf/results
{
  echo "dep=${DEP}"
  echo "mode=${MODE}"
  echo "p99_ms=${P99_MS}"
  echo "error_rate=${ERROR_RATE}"
  echo "applied_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} >perf/results/active-fault.txt
echo "[inject-fault] active fault recorded in perf/results/active-fault.txt"
