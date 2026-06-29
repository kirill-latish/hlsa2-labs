#!/usr/bin/env bash
# run-bench-staleness - run a writer/reader race and measure how often
# the cache serves a version older than the system of record.
#
# Writers bump versions on a small hot keyset; readers sample those keys
# through the cache and compare each read to /source. STRATEGY selects
# the app's invalidation behaviour:
#   ttl-only            cache holds the old value until its TTL lapses
#   explicit-invalidate the write deletes the cached key immediately
#
# Inputs (env): STRATEGY TTL DURATION LABEL RATE
# Writes: perf/results/staleness/<sub>/{summary.json,app-metrics-before.txt,app-metrics.txt,meta.json}
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
cd "${LAB_ROOT}"

STRATEGY="${STRATEGY:-ttl-only}"
TTL="${TTL:-60s}"
DURATION="${DURATION:-5m}"
RATE="${RATE:-500}"
TTL_S="$(dur_to_seconds "${TTL}")"
DURATION_S="$(dur_to_seconds "${DURATION}")"

case "${STRATEGY}" in
  ttl-only)            SUB="ttl-only" ;;
  explicit-invalidate) SUB="explicit" ;;
  *) echo "ERROR: STRATEGY must be ttl-only|explicit-invalidate" >&2; exit 2 ;;
esac
LABEL="${LABEL:-staleness-${SUB}}"

RUN_DIR="perf/results/staleness/${SUB}"
mkdir -p "${RUN_DIR}"

echo "[bench-staleness] strategy=${STRATEGY} ttl=${TTL_S}s duration=${DURATION_S}s"
app_config "$(jq -n --arg s "${STRATEGY}" --argjson t "${TTL_S}" '{invalidation:$s, cache_ttl_seconds:$t}')" >/dev/null
echo "[bench-staleness] active config -> $(curl -fsS "${APP}/admin/config")"

echo "[bench-staleness] flushing cache for a clean race"
curl -fsS -X POST "${APP}/admin/flush" >/dev/null

snapshot_app_metrics "${RUN_DIR}/app-metrics-before.txt"

curl -fsS -X POST -H 'content-type: application/json' \
  -d "$(jq -n --arg s "${STRATEGY}" --argjson r "${RATE}" --argjson dur "${DURATION_S}" --arg l "${LABEL}" \
          '{mode:"staleness",strategy:$s,rate_rps:$r,duration_s:$dur,label:$l,writers:4}')" \
  "${LOADGEN}/start" >/dev/null

poll_until_done "${DURATION_S}"

curl -fsS "${LOADGEN}/summary" >"${RUN_DIR}/summary.json"
snapshot_app_metrics "${RUN_DIR}/app-metrics.txt"
jq -n \
  --arg label "${LABEL}" --arg strategy "${STRATEGY}" \
  --argjson ttl "${TTL_S}" --argjson dur "${DURATION_S}" --argjson rate "${RATE}" \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{label:$label, strategy:$strategy, ttl_s:$ttl, duration_s:$dur, rate_rps:$rate, captured_at:$captured_at}' \
  >"${RUN_DIR}/meta.json"

echo "[bench-staleness] wrote ${RUN_DIR}/"
echo "Next: make analyze-staleness"
