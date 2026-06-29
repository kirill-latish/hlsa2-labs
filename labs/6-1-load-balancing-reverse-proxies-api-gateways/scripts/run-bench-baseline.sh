#!/usr/bin/env bash
# run-bench-baseline - drive the edge via loadgen at constant RPS and
# capture the edge-overhead histogram SEPARATELY from total latency.
#
# Inputs (env):
#   RUNS        runs to execute (default 3, satisfies the 3-run rubric)
#   LABEL       subdir under perf/results/ (default baseline)
#   RATE        offered RPS per run (default 200)
#   DURATION    duration per run, accepts 5m/30s/180 (default 5m)
#   SLOW_RATIO  fraction of slow requests (default 0.2)
#
# Writes for each run:
#   perf/results/<LABEL>/runN/summary.json        loadgen /summary
#   perf/results/<LABEL>/runN/edge-metrics.txt    edge /metrics
#   perf/results/<LABEL>/runN/meta.json
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

_RUNS="${RUNS:-}"; _LABEL="${LABEL:-}"; _RATE="${RATE:-}"
_DURATION="${DURATION:-}"; _SLOW="${SLOW_RATIO:-}"

if [[ -f .env ]]; then set -a; source .env; set +a; fi

RUNS="${_RUNS:-3}"
LABEL="${_LABEL:-baseline}"
RATE="${_RATE:-200}"
DURATION="${_DURATION:-5m}"
SLOW_RATIO="${_SLOW:-0.2}"
DURATION_S="$(to_seconds "${DURATION}")"

EDGE="http://localhost:${LAB_EDGE_PORT:-8080}"
LOADGEN="http://localhost:${LAB_LOADGEN_PORT:-8090}"

OUT_BASE="perf/results/${LABEL}"
mkdir -p "${OUT_BASE}"

echo "[run-bench-baseline] edge config -> $(curl -fsS "${EDGE}/admin/config")"

for i in $(seq 1 "${RUNS}"); do
  RUN_DIR="${OUT_BASE}/run${i}"
  mkdir -p "${RUN_DIR}"
  echo
  echo "================================================================="
  echo "[run-bench-baseline] LABEL=${LABEL} run=${i}/${RUNS} rate=${RATE} duration=${DURATION_S}s slow_ratio=${SLOW_RATIO}"
  echo "================================================================="

  loadgen_start "${LOADGEN}" "${RATE}" "${DURATION_S}" "${LABEL}-run${i}" "${SLOW_RATIO}"
  poll_until_done "${LOADGEN}" "${DURATION_S}"

  curl -fsS "${LOADGEN}/summary?label=${LABEL}-run${i}" >"${RUN_DIR}/summary.json"
  curl -fsS "${EDGE}/metrics" >"${RUN_DIR}/edge-metrics.txt"
  jq -n \
    --arg label "${LABEL}" --arg run "${i}" \
    --argjson rate "${RATE}" --argjson dur "${DURATION_S}" \
    --argjson slow "${SLOW_RATIO}" \
    --argjson cfg "$(curl -fsS "${EDGE}/admin/config")" \
    --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{label:$label, run:$run|tonumber, rate_rps:$rate, duration_s:$dur, slow_ratio:$slow, edge_config:$cfg, captured_at:$captured_at}' \
    >"${RUN_DIR}/meta.json"
  echo "[run-bench-baseline] wrote ${RUN_DIR}/"
done

echo
echo "Done. Next: make analyze-baseline LABEL=${LABEL}"
