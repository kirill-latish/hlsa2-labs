#!/usr/bin/env bash
# run-bench-overload - drive the gateway past saturation with loadgen
# retries enabled. Used in step 7 to provoke the retry storm in the
# "storm" run, and to demonstrate retry-budget + load-shed taming the
# storm in the "tamed" run. Both runs MUST use the same arrival ramp.
#
# Inputs (env):
#   LABEL          subdir under perf/results/ (default storm or tamed)
#   RATE           PEAK offered RPS (default 1000)
#   DURATION_S     total duration including ramp (default 240)
#   WARMUP_S       (default 30)
#   RETRY_BUDGET LOAD_SHED BULKHEAD CIRCUIT_BREAKER FALLBACK - logged
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

# Snapshot caller-provided knobs BEFORE sourcing .env (same rationale
# as run-bench-baseline.sh).
_LABEL="${LABEL:-}"
_RATE="${RATE:-}"
_DURATION_S="${DURATION_S:-}"
_WARMUP_S="${WARMUP_S:-}"

if [[ -f .env ]]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi

LABEL="${_LABEL:-${LABEL:-storm}}"
RATE="${_RATE:-${RATE:-1000}}"
DURATION_S="${_DURATION_S:-${DURATION_S:-240}}"
WARMUP_S="${_WARMUP_S:-${WARMUP_S:-30}}"
LOADGEN="http://localhost:${LAB_LOADGEN_PORT:-8090}"
GATEWAY="http://localhost:${LAB_GATEWAY_PORT:-8080}"

OUT_DIR="perf/results/overload/${LABEL}"
mkdir -p "${OUT_DIR}"

controls_json() {
  jq -n \
    --arg b "${BULKHEAD:-off}" \
    --arg cb "${CIRCUIT_BREAKER:-off}" \
    --arg f "${FALLBACK:-off}" \
    --arg rb "${RETRY_BUDGET:-off}" \
    --arg ls "${LOAD_SHED:-off}" \
    '{BULKHEAD:$b, CIRCUIT_BREAKER:$cb, FALLBACK:$f, RETRY_BUDGET:$rb, LOAD_SHED:$ls}'
}

push_gateway_controls "${GATEWAY}" \
  "${BULKHEAD:-off}" "${CIRCUIT_BREAKER:-off}" "${FALLBACK:-off}" \
  "${RETRY_BUDGET:-off}" "${LOAD_SHED:-off}"

echo
echo "[run-bench-overload] LABEL=${LABEL} peak_rate=${RATE} duration=${DURATION_S}s controls=$(controls_json | jq -c .)"
echo "[run-bench-overload] gateway controls -> $(curl -fsS "${GATEWAY}/admin/config")"

curl -fsS -X POST -H 'content-type: application/json' \
  -d "$(jq -n --argjson r ${RATE} --argjson d ${DURATION_S} --arg l "${LABEL}" \
          '{rate_rps:$r,duration_s:$d,label:$l,profile:"overload",in_loadgen_retries:2}')" \
  "${LOADGEN}/start" >/dev/null

poll_until_done "${LOADGEN}" "${DURATION_S}"

curl -fsS "${LOADGEN}/summary?label=${LABEL}" >"${OUT_DIR}/summary.json"
curl -fsS "${GATEWAY}/metrics" >"${OUT_DIR}/gateway-metrics.txt"
jq -n \
  --arg label "${LABEL}" \
  --argjson rate "${RATE}" \
  --argjson dur "${DURATION_S}" \
  --argjson warmup "${WARMUP_S}" \
  --argjson controls "$(controls_json)" \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{label:$label, profile:"overload", rate_rps:$rate, duration_s:$dur, warmup_s:$warmup, controls:$controls, captured_at:$captured_at}' \
  >"${OUT_DIR}/meta.json"

echo "[run-bench-overload] wrote ${OUT_DIR}/"
echo
echo "Done. Re-run with LABEL=tamed RETRY_BUDGET=on LOAD_SHED=on, then:"
echo "  make analyze-overload"
