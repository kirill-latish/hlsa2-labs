#!/usr/bin/env bash
# run-bench-baseline - drive the representative request mix through the
# edge at a constant rate, RUNS times, snapshotting every node's
# /metrics before AND after each run so the analyzer can compute per-run
# deltas (hit ratio by request AND by bytes, offload, cache-status).
#
# Inputs (env): RUNS, DURATION (e.g. 5m), LABEL, RATE.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

RUNS="${RUNS:-3}"
DURATION="${DURATION:-3m}"
LABEL="${LABEL:-baseline}"
RATE="${RATE:-200}"
DUR_S="$(parse_duration "${DURATION}")"

OUT_BASE="perf/results/${LABEL}"
mkdir -p "${OUT_BASE}"

# Baseline = the recommended healthy edge config. Flip the PoPs back to
# it so a previous experiment's leftover config can't pollute the run.
push_config_pops '{"cache_key_mode":"stripped-allowlist","allowlist":["v"],"ttl_seconds":60,"request_collapsing":true,"stale_if_error":false,"personalized_mode":"private-no-store","shield_routing":true}'
echo "[bench-baseline] PoP config -> $(curl -fsS "${POP1_URL}/admin/config" | jq -c '.config')"

for i in $(seq 1 "${RUNS}"); do
  RUN_DIR="${OUT_BASE}/run${i}"
  mkdir -p "${RUN_DIR}"
  echo
  echo "================================================================="
  echo "[bench-baseline] LABEL=${LABEL} run=${i}/${RUNS} rate=${RATE} duration=${DUR_S}s"
  echo "================================================================="

  flush_edge
  snapshot_metrics "${RUN_DIR}" before
  drive_load "${RATE}" "${DUR_S}" "${LABEL}-run${i}"
  snapshot_metrics "${RUN_DIR}" after

  curl -fsS "${LOADGEN_URL}/summary" >"${RUN_DIR}/summary.json"
  jq -n \
    --arg label "${LABEL}" --arg run "${i}" \
    --argjson rate "${RATE}" --argjson dur "${DUR_S}" \
    --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{label:$label, run:($run|tonumber), rate_rps:$rate, duration_s:$dur, captured_at:$captured_at}' \
    >"${RUN_DIR}/meta.json"
  echo "[bench-baseline] wrote ${RUN_DIR}/"
done

echo
echo "Done. Next: make analyze-baseline LABEL=${LABEL}"
