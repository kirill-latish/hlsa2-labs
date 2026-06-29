#!/usr/bin/env bash
# inject-dependency-hiccup - briefly make Postgres unavailable while
# driving load. With deep health checks this takes EVERY backend down at
# once (the cascading failure); with shallow checks the service rides it
# out.
#
# Inputs (env):
#   DURATION  how long Postgres is paused, accepts 5s/5 (default 5s)
#   LABEL     subdir under perf/results/ (default healthcheck)
#   RATE      offered RPS (default 150)
#
# Writes perf/results/<LABEL>/:
#   edge-metrics-start.txt / edge-metrics-end.txt, summary.json, meta.json
#   (meta records min_healthy_during - 0 proves the cascade)
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

_DURATION="${DURATION:-}"; _LABEL="${LABEL:-}"; _RATE="${RATE:-}"
if [[ -f .env ]]; then set -a; source .env; set +a; fi

DURATION="${_DURATION:-5s}"
LABEL="${_LABEL:-healthcheck}"
RATE="${_RATE:-150}"
HICCUP_S="$(to_seconds "${DURATION}")"
RUN_S=$(( HICCUP_S + 20 ))
HICCUP_AFTER_S="${HICCUP_AFTER_S:-8}"

EDGE="http://localhost:${LAB_EDGE_PORT:-8080}"
LOADGEN="http://localhost:${LAB_LOADGEN_PORT:-8090}"
OUT="perf/results/${LABEL}"
mkdir -p "${OUT}"

DEPTH="$(curl -fsS "${EDGE}/admin/config" | jq -r '.health_depth')"
echo "[inject-dependency-hiccup] current health_depth=${DEPTH} hiccup=${HICCUP_S}s"

curl -fsS "${EDGE}/metrics" >"${OUT}/edge-metrics-start.txt"

echo "[inject-dependency-hiccup] starting load: rate=${RATE} duration=${RUN_S}s"
loadgen_start "${LOADGEN}" "${RATE}" "${RUN_S}" "${LABEL}" "0.1"

sleep "${HICCUP_AFTER_S}"
echo "[inject-dependency-hiccup] pausing postgres for ${HICCUP_S}s ..."
docker compose pause postgres

# Sample the proxy's healthy-backend count throughout the hiccup so we
# can prove the deep-check cascade (min goes to 0) vs shallow (stays 4).
MIN_HEALTHY=4
END=$(( $(date +%s) + HICCUP_S ))
while [[ "$(date +%s)" -lt "${END}" ]]; do
  h="$(edge_status "${EDGE}" | jq -r '.healthy_backends' 2>/dev/null || echo 4)"
  if [[ "${h}" =~ ^[0-9]+$ ]] && [[ "${h}" -lt "${MIN_HEALTHY}" ]]; then
    MIN_HEALTHY="${h}"
  fi
  sleep 1
done

echo "[inject-dependency-hiccup] unpausing postgres"
docker compose unpause postgres

# Give the proxy a few cycles to recover before snapshotting.
sleep 8
poll_until_done "${LOADGEN}" "${RUN_S}"

curl -fsS "${EDGE}/metrics" >"${OUT}/edge-metrics-end.txt"
curl -fsS "${LOADGEN}/summary?label=${LABEL}" >"${OUT}/summary.json"
edge_status "${EDGE}" >"${OUT}/status-end.json"

jq -n \
  --arg label "${LABEL}" --arg depth "${DEPTH}" \
  --argjson hiccup "${HICCUP_S}" --argjson minh "${MIN_HEALTHY}" \
  --argjson rate "${RATE}" --argjson dur "${RUN_S}" \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{label:$label, health_depth:$depth, hiccup_s:$hiccup, min_healthy_during:$minh,
    rate_rps:$rate, duration_s:$dur, captured_at:$captured_at}' \
  >"${OUT}/meta.json"

echo "[inject-dependency-hiccup] min healthy backends during hiccup: ${MIN_HEALTHY}"
echo "[inject-dependency-hiccup] wrote ${OUT}/   Next: make analyze-healthcheck LABEL=${LABEL}"
