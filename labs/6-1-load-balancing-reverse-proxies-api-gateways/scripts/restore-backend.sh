#!/usr/bin/env bash
# restore-backend - clear the injected fault on a backend (via the edge
# admin) so it rejoins rotation after the next healthy probe.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
if [[ -f .env ]]; then set -a; source .env; set +a; fi

BACKEND="${BACKEND:?BACKEND=backend-1..4 is required}"
EDGE="http://localhost:${LAB_EDGE_PORT:-8080}"

echo "[restore-backend] restoring ${BACKEND}"
curl -fsS -X POST "${EDGE}/admin/backend/${BACKEND}/restore"
echo
echo "[restore-backend] it rejoins rotation on the next successful health probe."
