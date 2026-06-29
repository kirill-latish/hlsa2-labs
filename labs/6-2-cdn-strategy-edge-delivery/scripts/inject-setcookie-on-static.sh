#!/usr/bin/env bash
# inject-setcookie-on-static - the 'caching nothing' silent failure. Tell
# the origin to glue a Set-Cookie onto static responses; the edge is then
# forced to BYPASS what should be perfectly cacheable. Latency and uptime
# stay normal - only the cache-status distribution reveals the collapse.
#
# Drives a short burst of static traffic with the injection on, snapshot
# before/after, so analyze-cache-status sees the BYPASS spike.
# Inputs (env): LABEL, DURATION, RATE.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

LABEL="${LABEL:-bypass}"
DURATION="${DURATION:-90s}"
RATE="${RATE:-200}"
DUR_S="$(parse_duration "${DURATION}")"

RUN_DIR="perf/results/${LABEL}/run1"
mkdir -p "${RUN_DIR}"

echo "[inject-setcookie-on-static] LABEL=${LABEL} - enabling Set-Cookie on static at the origin"
push_config_origin '{"setcookie_on_static":true}'
flush_edge

snapshot_metrics "${RUN_DIR}" before
drive_load "${RATE}" "${DUR_S}" "${LABEL}"
snapshot_metrics "${RUN_DIR}" after

curl -fsS "${LOADGEN_URL}/summary" >"${RUN_DIR}/summary.json"
jq -n --arg label "${LABEL}" --argjson dur "${DUR_S}" \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{label:$label, duration_s:$dur, injection:"setcookie_on_static", captured_at:$captured_at}' \
  >"${RUN_DIR}/meta.json"

echo "[inject-setcookie-on-static] wrote ${RUN_DIR}/"
echo "NOTE: the injection is still ON. Clear it with:"
echo "  curl -X POST -d '{\"setcookie_on_static\":false}' ${ORIGIN_URL}/admin/config"
echo "Next: make analyze-cache-status LABEL=${LABEL}"
