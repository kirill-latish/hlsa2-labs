#!/usr/bin/env bash
# Drive bench-polyglot for $RUNS iterations. FRESHNESS picks the policy
# under test; LSN_WAIT_MAX_MS bounds the lsn-wait policy.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/_lib.sh"

RUNS="${RUNS:-3}"
DURATION="${DURATION:-3m}"
WARMUP_S="${WARMUP_S:-5}"
FRESHNESS="${FRESHNESS:-lsn-wait}"
LSN_WAIT_MAX_MS="${LSN_WAIT_MAX_MS:-1500}"

case "${DURATION}" in
  *m) dur_seconds=$(( ${DURATION%m} * 60 )) ;;
  *s) dur_seconds="${DURATION%s}" ;;
   *) dur_seconds="${DURATION}" ;;
esac

OUT_BASE="${RESULTS_ROOT}/polyglot/${FRESHNESS}"
mkdir -p "${OUT_BASE}"

for n in $(seq 1 "${RUNS}"); do
  out_dir="${OUT_BASE}/run-${n}"
  mkdir -p "${out_dir}"
  log_step "bench-polyglot run ${n}/${RUNS} (freshness=${FRESHNESS} duration=${DURATION})"
  docker_run_oneshot bench-polyglot \
    --freshness "${FRESHNESS}" \
    --duration-seconds "${dur_seconds}" \
    --warmup-seconds "${WARMUP_S}" \
    --lsn-wait-max-ms "${LSN_WAIT_MAX_MS}" \
    --out "/perf/results/polyglot/${FRESHNESS}/run-${n}"
done

log_step "bench-polyglot runs complete"
ls -1 "${OUT_BASE}"
