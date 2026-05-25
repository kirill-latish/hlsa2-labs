#!/usr/bin/env bash
# clear-fault - DELETE the active fault for a dep.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"

if [[ -f .env ]]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi

DEP="${DEP:?DEP=price|cart|recommendations|reviews|recently-viewed is required}"
URL="http://localhost:${LAB_FAULT_INJECTOR_PORT:-9000}/faults/${DEP}"
echo "[clear-fault] dep=${DEP}"
curl -fsS -X DELETE "${URL}"
echo
rm -f perf/results/active-fault.txt || true
