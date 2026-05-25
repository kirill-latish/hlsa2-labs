#!/usr/bin/env bash
# run-bench-baseline - drive the gateway via loadgen at constant RPS.
#
# Inputs (env):
#   RUNS           how many runs to execute (default 3 to satisfy 3-run rubric)
#   LABEL          subdir under perf/results/ (default baseline)
#   RATE           offered RPS per run (default 200, see perf/workload.json)
#   DURATION_S     duration per run (default 180)
#   WARMUP_S       warmup to skip in analyses (default 30; loadgen still drives)
#   BULKHEAD CIRCUIT_BREAKER FALLBACK RETRY_BUDGET LOAD_SHED  - logged in meta.json
#
# Writes for each run:
#   perf/results/<LABEL>/runN/summary.json       loadgen /summary snapshot
#   perf/results/<LABEL>/runN/gateway-metrics.txt curl :8080/metrics
#   perf/results/<LABEL>/runN/meta.json
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

# Snapshot caller-provided workload knobs BEFORE sourcing .env so a
# stale .env can never silently override values the make wrapper
# (or the topic guide's command line) just passed.
_RUNS="${RUNS:-}"
_LABEL="${LABEL:-}"
_RATE="${RATE:-}"
_DURATION_S="${DURATION_S:-}"
_WARMUP_S="${WARMUP_S:-}"

if [[ -f .env ]]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi

# Restore caller-provided values (.env can only contribute defaults,
# not overrides).
RUNS="${_RUNS:-${RUNS:-3}}"
LABEL="${_LABEL:-${LABEL:-baseline}}"
RATE="${_RATE:-${RATE:-200}}"
DURATION_S="${_DURATION_S:-${DURATION_S:-180}}"
WARMUP_S="${_WARMUP_S:-${WARMUP_S:-30}}"
LOADGEN="http://localhost:${LAB_LOADGEN_PORT:-8090}"
GATEWAY="http://localhost:${LAB_GATEWAY_PORT:-8080}"

OUT_BASE="perf/results/${LABEL}"
mkdir -p "${OUT_BASE}"

# Capture the controls for meta.json so compare.sh can verify "same
# fault, different controls".
controls_json() {
  jq -n \
    --arg b "${BULKHEAD:-off}" \
    --arg cb "${CIRCUIT_BREAKER:-off}" \
    --arg f "${FALLBACK:-off}" \
    --arg rb "${RETRY_BUDGET:-off}" \
    --arg ls "${LOAD_SHED:-off}" \
    '{BULKHEAD:$b, CIRCUIT_BREAKER:$cb, FALLBACK:$f, RETRY_BUDGET:$rb, LOAD_SHED:$ls}'
}

active_fault_json() {
  if [[ -f perf/results/active-fault.txt ]]; then
    jq -Rn '[inputs|split("=")|{(.[0]):.[1]}]|add' <perf/results/active-fault.txt
  else
    echo '{}'
  fi
}

push_gateway_controls "${GATEWAY}" \
  "${BULKHEAD:-off}" "${CIRCUIT_BREAKER:-off}" "${FALLBACK:-off}" \
  "${RETRY_BUDGET:-off}" "${LOAD_SHED:-off}"
echo "[run-bench-baseline] gateway controls -> $(curl -fsS "${GATEWAY}/admin/config")"

for i in $(seq 1 "${RUNS}"); do
  RUN_DIR="${OUT_BASE}/run${i}"
  mkdir -p "${RUN_DIR}"
  echo
  echo "================================================================="
  echo "[run-bench-baseline] LABEL=${LABEL} run=${i}/${RUNS} rate=${RATE} duration=${DURATION_S}s"
  echo "================================================================="

  curl -fsS -X POST -H 'content-type: application/json' \
    -d "$(jq -n --argjson r ${RATE} --argjson d ${DURATION_S} --arg l "${LABEL}-run${i}" \
            '{rate_rps:$r,duration_s:$d,label:$l,profile:"baseline",in_loadgen_retries:0}')" \
    "${LOADGEN}/start" >/dev/null

  # Poll /state every 5s until running=false.
  poll_until_done "${LOADGEN}" "${DURATION_S}"

  # Snapshot the run summary + gateway metrics.
  curl -fsS "${LOADGEN}/summary?label=${LABEL}-run${i}" >"${RUN_DIR}/summary.json"
  curl -fsS "${GATEWAY}/metrics" >"${RUN_DIR}/gateway-metrics.txt"
  jq -n \
    --arg label "${LABEL}" \
    --arg run "${i}" \
    --argjson rate "${RATE}" \
    --argjson dur "${DURATION_S}" \
    --argjson warmup "${WARMUP_S}" \
    --argjson controls "$(controls_json)" \
    --argjson fault "$(active_fault_json)" \
    --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{label:$label, run:$run|tonumber, rate_rps:$rate, duration_s:$dur, warmup_s:$warmup, controls:$controls, active_fault:$fault, captured_at:$captured_at}' \
    >"${RUN_DIR}/meta.json"
  echo "[run-bench-baseline] wrote ${RUN_DIR}/"
done

echo
echo "Done. Next:"
echo "  make analyze-baseline                         # for label=baseline"
echo "  make analyze-blast-radius LABEL=${LABEL}      # if you injected a fault first"
