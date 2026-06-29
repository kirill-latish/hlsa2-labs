#!/usr/bin/env bash
# run-bench-cachekey - measure the edge under a chosen cache-key policy.
#   KEY=full-querystring  keys on the whole query string (tracking params
#                         fragment the cache -> hit ratio collapses,
#                         cardinality explodes).
#   KEY=stripped          keys on path + allowlist only (tracking params
#                         stripped -> hits recover, cardinality collapses).
#
# Inputs (env): KEY, DURATION, LABEL, RATE.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

KEY="${KEY:-stripped}"
DURATION="${DURATION:-3m}"
LABEL="${LABEL:-cachekey}"
RATE="${RATE:-200}"
DUR_S="$(parse_duration "${DURATION}")"

case "${KEY}" in
  full-querystring) MODE="full-querystring" ;;
  stripped|stripped-allowlist) MODE="stripped-allowlist" ;;
  *) echo "ERROR: KEY must be full-querystring|stripped" >&2; exit 2 ;;
esac

push_config_pops "$(jq -n --arg m "${MODE}" '{cache_key_mode:$m, allowlist:["v"]}')"
echo "[bench-cachekey] KEY=${KEY} -> PoP cache_key_mode=${MODE}"

RUN_DIR="perf/results/${LABEL}/run1"
mkdir -p "${RUN_DIR}"

flush_edge
snapshot_metrics "${RUN_DIR}" before
drive_load "${RATE}" "${DUR_S}" "${LABEL}"
snapshot_metrics "${RUN_DIR}" after

curl -fsS "${LOADGEN_URL}/summary" >"${RUN_DIR}/summary.json"
jq -n --arg label "${LABEL}" --arg key "${KEY}" --arg mode "${MODE}" \
  --argjson dur "${DUR_S}" --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{label:$label, key:$key, cache_key_mode:$mode, duration_s:$dur, captured_at:$captured_at}' \
  >"${RUN_DIR}/meta.json"

echo "[bench-cachekey] wrote ${RUN_DIR}/"
echo "Next: make analyze-baseline LABEL=${LABEL}   (or)   make compare-cachekey BEFORE=... AFTER=..."
