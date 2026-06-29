#!/usr/bin/env bash
# seed - prove the edge routes to all backends with a handful of fast +
# slow requests, and print which backend served each (X-Backend-Id).
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
if [[ -f .env ]]; then set -a; source .env; set +a; fi

EDGE="http://localhost:${LAB_EDGE_PORT:-8080}"

echo "[seed] sending 8 fast + 4 slow requests through ${EDGE}/work ..."
for i in $(seq 1 8); do
  curl -fsS -D - -o /dev/null "${EDGE}/work?cost=fast" 2>/dev/null \
    | awk 'BEGIN{IGNORECASE=1} /^X-Backend-Id:/ {print "  fast  -> " $2}'
done
for i in $(seq 1 4); do
  curl -fsS -D - -o /dev/null "${EDGE}/work?cost=slow" 2>/dev/null \
    | awk 'BEGIN{IGNORECASE=1} /^X-Backend-Id:/ {print "  slow  -> " $2}'
done

echo
echo "[seed] proxy view of backend health:"
curl -fsS "${EDGE}/admin/status" | jq '{algo, health_depth, healthy_backends, backends: [.backends[] | {id, healthy, requests}]}'
