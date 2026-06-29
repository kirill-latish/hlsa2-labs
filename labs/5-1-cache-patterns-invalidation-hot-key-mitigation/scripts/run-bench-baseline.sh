#!/usr/bin/env bash
# run-bench-baseline - drive the app's /read endpoint via the loadgen
# under a Zipfian (or uniform) key distribution.
#
# Inputs (env):
#   DIST      zipf|uniform   (default zipf - uniform hides cache patterns)
#   RUNS      how many runs  (default 3 to satisfy the 3-run rubric)
#   DURATION  per-run length (default 5m; accepts 30s/5m/1h)
#   LABEL     subdir under perf/results/ (default baseline)
#   RATE      offered RPS per run (default 2000)
#
# Writes for each run:
#   perf/results/<LABEL>/runN/summary.json            loadgen /summary
#   perf/results/<LABEL>/runN/app-metrics-before.txt  app /metrics pre-run
#   perf/results/<LABEL>/runN/app-metrics.txt         app /metrics post-run
#   perf/results/<LABEL>/runN/meta.json
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
cd "${LAB_ROOT}"

DIST="${DIST:-zipf}"
RUNS="${RUNS:-3}"
DURATION="${DURATION:-5m}"
LABEL="${LABEL:-baseline}"
RATE="${RATE:-2000}"
DURATION_S="$(dur_to_seconds "${DURATION}")"

OUT_BASE="perf/results/${LABEL}"
mkdir -p "${OUT_BASE}"

echo "[bench-baseline] app config -> $(curl -fsS "${APP}/admin/config")"

for i in $(seq 1 "${RUNS}"); do
  RUN_DIR="${OUT_BASE}/run${i}"
  mkdir -p "${RUN_DIR}"
  echo
  echo "================================================================="
  echo "[bench-baseline] LABEL=${LABEL} run=${i}/${RUNS} dist=${DIST} rate=${RATE} duration=${DURATION_S}s"
  echo "================================================================="

  snapshot_app_metrics "${RUN_DIR}/app-metrics-before.txt"

  curl -fsS -X POST -H 'content-type: application/json' \
    -d "$(jq -n --arg d "${DIST}" --argjson r "${RATE}" --argjson dur "${DURATION_S}" --arg l "${LABEL}-run${i}" \
            '{mode:"baseline",dist:$d,rate_rps:$r,duration_s:$dur,label:$l}')" \
    "${LOADGEN}/start" >/dev/null

  poll_until_done "${DURATION_S}"

  curl -fsS "${LOADGEN}/summary" >"${RUN_DIR}/summary.json"
  snapshot_app_metrics "${RUN_DIR}/app-metrics.txt"
  jq -n \
    --arg label "${LABEL}" --arg run "${i}" --arg dist "${DIST}" \
    --argjson rate "${RATE}" --argjson dur "${DURATION_S}" \
    --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{label:$label, run:$run|tonumber, dist:$dist, rate_rps:$rate, duration_s:$dur, captured_at:$captured_at}' \
    >"${RUN_DIR}/meta.json"
  echo "[bench-baseline] wrote ${RUN_DIR}/"
done

echo
echo "Done. Next: make analyze-baseline LABEL=${LABEL}"
