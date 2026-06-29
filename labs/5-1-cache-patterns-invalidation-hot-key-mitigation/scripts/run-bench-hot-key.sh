#!/usr/bin/env bash
# run-bench-hot-key - drive Zipfian load with a configurable fraction of
# traffic pinned to the active hot key (set via make inject-hot-key).
# Per-node Redis ops in the captured app metrics reveal the imbalance.
#
# Inputs (env): DURATION LABEL RATE DIST
# Writes: perf/results/<LABEL>/run1/{summary.json,app-metrics-before.txt,app-metrics.txt,meta.json}
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
cd "${LAB_ROOT}"

DURATION="${DURATION:-3m}"
LABEL="${LABEL:-hot-baseline}"
RATE="${RATE:-2000}"
DIST="${DIST:-zipf}"
DURATION_S="$(dur_to_seconds "${DURATION}")"

HOTFILE="perf/results/active-hotkey.txt"
if [[ ! -f "${HOTFILE}" ]]; then
  echo "ERROR: no active hot key. Run: make inject-hot-key KEY=celebrity-1 WEIGHT=0.4" >&2
  exit 2
fi
KEY="$(grep '^key=' "${HOTFILE}" | cut -d= -f2)"
WEIGHT="$(grep '^weight=' "${HOTFILE}" | cut -d= -f2)"

RUN_DIR="perf/results/${LABEL}/run1"
mkdir -p "${RUN_DIR}"

echo "[bench-hot-key] key=${KEY} weight=${WEIGHT} rate=${RATE} duration=${DURATION_S}s"
echo "[bench-hot-key] active config -> $(curl -fsS "${APP}/admin/config")"

snapshot_app_metrics "${RUN_DIR}/app-metrics-before.txt"

curl -fsS -X POST -H 'content-type: application/json' \
  -d "$(jq -n --arg k "${KEY}" --argjson w "${WEIGHT}" --arg d "${DIST}" \
          --argjson r "${RATE}" --argjson dur "${DURATION_S}" --arg l "${LABEL}" \
          '{mode:"hotkey",hot_key:$k,hot_weight:$w,dist:$d,rate_rps:$r,duration_s:$dur,label:$l}')" \
  "${LOADGEN}/start" >/dev/null

poll_until_done "${DURATION_S}"

curl -fsS "${LOADGEN}/summary" >"${RUN_DIR}/summary.json"
snapshot_app_metrics "${RUN_DIR}/app-metrics.txt"
jq -n \
  --arg label "${LABEL}" --arg key "${KEY}" --argjson weight "${WEIGHT}" \
  --argjson rate "${RATE}" --argjson dur "${DURATION_S}" --arg dist "${DIST}" \
  --argjson cfg "$(curl -fsS "${APP}/admin/config")" \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{label:$label, hot_key:$key, hot_weight:$weight, dist:$dist, rate_rps:$rate, duration_s:$dur, config:$cfg, captured_at:$captured_at}' \
  >"${RUN_DIR}/meta.json"

echo "[bench-hot-key] wrote ${RUN_DIR}/"
