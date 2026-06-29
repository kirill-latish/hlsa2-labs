#!/usr/bin/env bash
# inject-backend-failure - drive steady load, kill one backend mid-run
# via the edge admin (which forwards the fault to the backend so the
# proxy still has to DETECT it through health checks), and capture the
# failover timing + dropped requests.
#
# Inputs (env):
#   BACKEND  backend-1..4 (required)
#   LABEL    subdir under perf/results/ (default failover-baseline)
#   RATE     offered RPS (default 150)
#   DURATION accepts 90s/2m/90 (default 90s)
#
# Writes perf/results/<LABEL>/:
#   edge-metrics-start.txt / edge-metrics-end.txt
#   status-start.json / status-end.json
#   summary.json, meta.json (interval, threshold, measured detection)
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

_BACKEND="${BACKEND:-}"; _LABEL="${LABEL:-}"; _RATE="${RATE:-}"; _DURATION="${DURATION:-}"
if [[ -f .env ]]; then set -a; source .env; set +a; fi

BACKEND="${_BACKEND:?BACKEND=backend-1..4 is required}"
LABEL="${_LABEL:-failover-baseline}"
RATE="${_RATE:-150}"
DURATION="${_DURATION:-90s}"
DURATION_S="$(to_seconds "${DURATION}")"
INJECT_AFTER_S="${INJECT_AFTER_S:-15}"

EDGE="http://localhost:${LAB_EDGE_PORT:-8080}"
LOADGEN="http://localhost:${LAB_LOADGEN_PORT:-8090}"
OUT="perf/results/${LABEL}"
mkdir -p "${OUT}"

CFG="$(curl -fsS "${EDGE}/admin/config")"
INTERVAL_MS="$(echo "${CFG}" | jq -r '.health_interval_ms')"
THRESHOLD="$(echo "${CFG}" | jq -r '.failure_threshold')"
echo "[inject-backend-failure] edge config: ${CFG}"
echo "[inject-backend-failure] expected detection ~ interval*threshold = $(( INTERVAL_MS * THRESHOLD / 1000 ))s"

curl -fsS "${EDGE}/metrics" >"${OUT}/edge-metrics-start.txt"
edge_status "${EDGE}" >"${OUT}/status-start.json"

echo "[inject-backend-failure] starting load: rate=${RATE} duration=${DURATION_S}s"
loadgen_start "${LOADGEN}" "${RATE}" "${DURATION_S}" "${LABEL}" "0.1"

sleep "${INJECT_AFTER_S}"
INJECT_AT="$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)"
INJECT_EPOCH="$(date +%s)"
echo "[inject-backend-failure] killing ${BACKEND} at ${INJECT_AT}"
curl -fsS -X POST "${EDGE}/admin/backend/${BACKEND}/fail" >/dev/null

# Measure the actual failover detection time: poll status until the edge
# marks the backend down.
DETECTED_EPOCH=""
DEADLINE=$(( $(date +%s) + DURATION_S ))
while [[ "$(date +%s)" -lt "${DEADLINE}" ]]; do
  healthy="$(edge_status "${EDGE}" | jq -r --arg b "${BACKEND}" '.backends[] | select(.id==$b) | .healthy')"
  if [[ "${healthy}" == "false" ]]; then
    DETECTED_EPOCH="$(date +%s)"
    break
  fi
  sleep 1
done

DETECTION_S="null"
if [[ -n "${DETECTED_EPOCH}" ]]; then
  DETECTION_S=$(( DETECTED_EPOCH - INJECT_EPOCH ))
  echo "[inject-backend-failure] edge marked ${BACKEND} DOWN after ~${DETECTION_S}s"
else
  echo "[inject-backend-failure] WARN: backend not marked down before run ended"
fi

poll_until_done "${LOADGEN}" "${DURATION_S}"

curl -fsS "${EDGE}/metrics" >"${OUT}/edge-metrics-end.txt"
edge_status "${EDGE}" >"${OUT}/status-end.json"
curl -fsS "${LOADGEN}/summary?label=${LABEL}" >"${OUT}/summary.json"

jq -n \
  --arg label "${LABEL}" --arg backend "${BACKEND}" \
  --argjson interval_ms "${INTERVAL_MS}" --argjson threshold "${THRESHOLD}" \
  --arg inject_at "${INJECT_AT}" --argjson detection_s "${DETECTION_S}" \
  --argjson rate "${RATE}" --argjson dur "${DURATION_S}" \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{label:$label, backend:$backend, health_interval_ms:$interval_ms, failure_threshold:$threshold,
    expected_detection_s:(($interval_ms*$threshold)/1000), measured_detection_s:$detection_s,
    inject_at:$inject_at, rate_rps:$rate, duration_s:$dur, captured_at:$captured_at}' \
  >"${OUT}/meta.json"

echo "[inject-backend-failure] wrote ${OUT}/"
echo "Next: make analyze-failover LABEL=${LABEL}   then   make restore-backend BACKEND=${BACKEND}"
