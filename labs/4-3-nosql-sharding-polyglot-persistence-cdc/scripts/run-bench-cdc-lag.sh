#!/usr/bin/env bash
# Drive bench-cdc-lag for $RUNS iterations. RATE accepts either a raw
# number ("100") or "1x"/"2x" multipliers of the workload.json default.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/_lib.sh"

RUNS="${RUNS:-3}"
DURATION="${DURATION:-5m}"
WARMUP_S="${WARMUP_S:-5}"
LABEL="${LABEL:-base}"
RATE_INPUT="${RATE:-1x}"

base_rate=$(jq -r '.cdc_lag.base_rate_per_sec' "${LAB_ROOT}/perf/workload.json")
case "${RATE_INPUT}" in
  1x) rate="${base_rate}" ;;
  2x) rate=$(( base_rate * 2 )) ;;
   *) rate="${RATE_INPUT}" ;;
esac

case "${DURATION}" in
  *m) dur_seconds=$(( ${DURATION%m} * 60 )) ;;
  *s) dur_seconds="${DURATION%s}" ;;
   *) dur_seconds="${DURATION}" ;;
esac

OUT_BASE="${RESULTS_ROOT}/cdc-lag/${LABEL}"
mkdir -p "${OUT_BASE}"

for n in $(seq 1 "${RUNS}"); do
  out_dir="${OUT_BASE}/run-${n}"
  mkdir -p "${out_dir}"
  log_step "bench-cdc-lag run ${n}/${RUNS} (rate=${rate} duration=${DURATION})"
  docker_run_oneshot bench-cdc-lag \
    --rate "${rate}" \
    --duration-seconds "${dur_seconds}" \
    --warmup-seconds "${WARMUP_S}" \
    --label "${LABEL}" \
    --out "/perf/results/cdc-lag/${LABEL}/run-${n}"
done

log_step "bench-cdc-lag runs complete"
ls -1 "${OUT_BASE}"
