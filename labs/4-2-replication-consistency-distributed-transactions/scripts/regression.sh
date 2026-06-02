#!/usr/bin/env bash
# Three-run regression for one $CANDIDATE. Re-runs the relevant bench
# on the baseline and on the candidate, and writes per-run summaries
# under perf/results/regression/<candidate>/{baseline,candidate}/runN.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

CANDIDATE="${CANDIDATE:-lsn-wait-on-raw}"
RUNS="${RUNS:-3}"
DURATION="${DURATION:-60s}"

OUT_BASE="${RESULTS_ROOT}/regression/${CANDIDATE}"
mkdir -p "${OUT_BASE}/baseline" "${OUT_BASE}/candidate"

case "${CANDIDATE}" in
  lsn-wait-on-raw)
    log_step "regression candidate=lsn-wait-on-raw RUNS=${RUNS}"
    for kind in baseline candidate; do
      mode=naive
      [ "${kind}" = "candidate" ] && mode=lsn-wait
      for i in $(seq 1 "${RUNS}"); do
        log_step "  ${kind} run${i} mode=${mode}"
        docker_run_oneshot raw-bench \
          --mode="${mode}" \
          --rate=200 \
          --duration="${DURATION}" \
          --warmup=5s \
          --workers=32 \
          --out="/perf/results/regression/${CANDIDATE}/${kind}/run${i}"
      done
    done
    ;;
  replace-2pc-with-saga)
    log_step "regression candidate=replace-2pc-with-saga RUNS=${RUNS}"
    for kind in baseline candidate; do
      mode=2pc
      [ "${kind}" = "candidate" ] && mode=saga
      for i in $(seq 1 "${RUNS}"); do
        log_step "  ${kind} run${i} mode=${mode}"
        docker_run_oneshot loadgen-saga \
          --mode="${mode}" \
          --rate=50 \
          --duration="${DURATION}" \
          --warmup=5s \
          --workers=16 \
          --out="/perf/results/regression/${CANDIDATE}/${kind}/run${i}" \
          --url="http://orchestrator:8080"
      done
    done
    ;;
  outbox-cdc)
    log_step "regression candidate=outbox-cdc - measuring relay latency"
    echo "Note: this candidate is a stub; pre-wired CDC is out of scope" \
         "for the lab. The make target still exercises the saga harness" \
         "so the regression decision is reproducible end-to-end." >&2
    for kind in baseline candidate; do
      for i in $(seq 1 "${RUNS}"); do
        log_step "  ${kind} run${i}"
        docker_run_oneshot loadgen-saga \
          --mode="saga" \
          --rate=50 \
          --duration="${DURATION}" \
          --warmup=5s \
          --workers=16 \
          --out="/perf/results/regression/${CANDIDATE}/${kind}/run${i}" \
          --url="http://orchestrator:8080"
      done
    done
    ;;
  *)
    echo "unknown CANDIDATE: ${CANDIDATE}" >&2
    exit 2
    ;;
esac

echo
echo "Wrote regression runs under ${OUT_BASE}/"
