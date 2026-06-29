#!/usr/bin/env bash
# edge-status - show the proxy's view of backend health + active config.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
if [[ -f .env ]]; then set -a; source .env; set +a; fi

EDGE="http://localhost:${LAB_EDGE_PORT:-8080}"
echo "[edge-status] GET ${EDGE}/admin/status"
curl -fsS "${EDGE}/admin/status" | jq .
