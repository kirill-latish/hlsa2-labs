#!/usr/bin/env bash
# Drive place-order via the 2PC path for $RUNS labelled runs.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

RUNS="${RUNS:-3}"
LABEL="${LABEL:-healthy}"
RATE="${RATE:-50}"
DURATION="${DURATION:-30s}"
WARMUP_S="${WARMUP_S:-5}"

BASE="${RESULTS_ROOT}/2pc/${LABEL}"
mkdir -p "${BASE}"

for i in $(seq 1 "${RUNS}"); do
  RUN_NAME="run${i}"
  log_step "2pc bench ${LABEL}/${RUN_NAME} rate=${RATE}/s duration=${DURATION}"
  docker_run_oneshot loadgen-saga \
    --mode=2pc \
    --rate="${RATE}" \
    --duration="${DURATION}" \
    --warmup="${WARMUP_S}s" \
    --workers=16 \
    --out="/perf/results/2pc/${LABEL}/${RUN_NAME}" \
    --url="http://orchestrator:8080"
done

# Aggregate.
python3 "${SCRIPTS_ROOT}/aggregate-runs.py" "${BASE}"
