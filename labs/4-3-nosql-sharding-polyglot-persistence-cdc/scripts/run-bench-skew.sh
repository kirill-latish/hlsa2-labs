#!/usr/bin/env bash
# Drive bench-skew for $RUNS iterations, each writing
# perf/results/skew/<label>/run-<n>/partition_metrics.json.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/_lib.sh"

SHARD_KEY="${SHARD_KEY:-candidate}"
RUNS="${RUNS:-3}"
DURATION="${DURATION:-5m}"
WRITE_RATE="${WRITE_RATE:-200}"
WARMUP_S="${WARMUP_S:-5}"
LABEL="${LABEL:-${SHARD_KEY}}"

# Convert Go-style duration into seconds for the bench binary.
case "${DURATION}" in
  *m) dur_seconds=$(( ${DURATION%m} * 60 )) ;;
  *s) dur_seconds="${DURATION%s}" ;;
   *) dur_seconds="${DURATION}" ;;
esac

OUT_BASE="${RESULTS_ROOT}/skew/${LABEL}"
mkdir -p "${OUT_BASE}"

for n in $(seq 1 "${RUNS}"); do
  out="${OUT_BASE}/run-${n}"
  mkdir -p "${out}"
  log_step "bench-skew run ${n}/${RUNS} -> ${out#${LAB_ROOT}/}"
  docker_run_oneshot bench-skew \
    --shard-key "${SHARD_KEY}" \
    --rate "${WRITE_RATE}" \
    --duration-seconds "${dur_seconds}" \
    --warmup-seconds "${WARMUP_S}" \
    --label "${LABEL}" \
    --out "/perf/results/skew/${LABEL}/run-${n}"
done

log_step "bench-skew runs complete"
ls -1 "${OUT_BASE}"
