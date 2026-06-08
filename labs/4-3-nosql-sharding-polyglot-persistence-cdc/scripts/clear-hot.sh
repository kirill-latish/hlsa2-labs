#!/usr/bin/env bash
# Clear the hot-entity injection.
set -euo pipefail
PORT="${LAB_FAULT_INJECTOR_PORT:-19001}"
SLOT="${SLOT:-hot}"
curl -fsS -X DELETE "http://localhost:${PORT}/faults/${SLOT}" -w '\n'
