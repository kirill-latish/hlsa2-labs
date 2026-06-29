#!/usr/bin/env bash
# run-inject-stampede - create one hot key, give it a short fixed TTL,
# and hammer it at HOT_RATE RPS. When the TTL expires under load, every
# concurrent miss arriving during the SoR fetch window stampedes the
# database. The fan-in ratio at expiry is the headline number.
#
# This script sets ONLY the TTL via /admin/config; the coalescing mode
# and jitter are left at whatever `make apply-fix` last set, so the same
# command reproduces the stampede before and after the fix.
#
# Inputs (env): TTL HOT_RATE DURATION LABEL
# Writes: perf/results/<LABEL>/run1/{summary.json,app-metrics-before.txt,app-metrics.txt,meta.json}
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
cd "${LAB_ROOT}"

TTL="${TTL:-30s}"
HOT_RATE="${HOT_RATE:-5000}"
DURATION="${DURATION:-3m}"
LABEL="${LABEL:-stampede-baseline}"
HOT_KEY="${HOT_KEY:-stampede-hot}"
TTL_S="$(dur_to_seconds "${TTL}")"
DURATION_S="$(dur_to_seconds "${DURATION}")"

RUN_DIR="perf/results/${LABEL}/run1"
mkdir -p "${RUN_DIR}"

echo "[inject-stampede] setting cache TTL=${TTL_S}s (coalescing/jitter unchanged)"
app_config "$(jq -n --argjson t "${TTL_S}" '{cache_ttl_seconds:$t}')" >/dev/null
echo "[inject-stampede] active config -> $(curl -fsS "${APP}/admin/config")"

echo "[inject-stampede] flushing cache so the hot key starts cold"
curl -fsS -X POST "${APP}/admin/flush" >/dev/null

snapshot_app_metrics "${RUN_DIR}/app-metrics-before.txt"

echo "[inject-stampede] driving key=${HOT_KEY} at ${HOT_RATE} rps for ${DURATION_S}s"
curl -fsS -X POST -H 'content-type: application/json' \
  -d "$(jq -n --arg k "${HOT_KEY}" --argjson r "${HOT_RATE}" --argjson dur "${DURATION_S}" --arg l "${LABEL}" \
          '{mode:"stampede",hot_key:$k,rate_rps:$r,duration_s:$dur,label:$l}')" \
  "${LOADGEN}/start" >/dev/null

poll_until_done "${DURATION_S}"

curl -fsS "${LOADGEN}/summary" >"${RUN_DIR}/summary.json"
snapshot_app_metrics "${RUN_DIR}/app-metrics.txt"
jq -n \
  --arg label "${LABEL}" --arg key "${HOT_KEY}" \
  --argjson ttl "${TTL_S}" --argjson rate "${HOT_RATE}" --argjson dur "${DURATION_S}" \
  --argjson cfg "$(curl -fsS "${APP}/admin/config")" \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{label:$label, hot_key:$key, ttl_s:$ttl, hot_rate:$rate, duration_s:$dur, config:$cfg, captured_at:$captured_at}' \
  >"${RUN_DIR}/meta.json"

echo "[inject-stampede] wrote ${RUN_DIR}/"
echo "Next: make analyze-stampede LABEL=${LABEL}"
