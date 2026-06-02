#!/usr/bin/env bash
# Drive a steady write workload on the primary while lag-sampler
# captures replica lag every 100ms. Produces N runs under
# perf/results/lag/runN/.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

RUNS="${RUNS:-3}"
DURATION="${DURATION:-5m}"
WRITE_RATE="${WRITE_RATE:-500}"
WARMUP_S="${WARMUP_S:-10}"

LAG_BASE="${RESULTS_ROOT}/lag"
mkdir -p "${LAG_BASE}"

# Convert DURATION (e.g. 5m, 30s) to seconds for the sleep+poll math.
case "${DURATION}" in
  *m) DURATION_S=$(( ${DURATION%m} * 60 ));;
  *s) DURATION_S=${DURATION%s};;
  *)  DURATION_S=${DURATION};;
esac

for i in $(seq 1 "${RUNS}"); do
  RUN_NAME="run${i}"
  RUN_DIR="${LAG_BASE}/${RUN_NAME}"
  mkdir -p "${RUN_DIR}"
  log_step "lag run ${i}/${RUNS} -> ${RUN_DIR}  rate=${WRITE_RATE}/s duration=${DURATION}"

  # Tell the sampler to rotate to a fresh CSV under perf/results/lag/runN.
  docker compose exec -T lag-sampler wget -qO- "http://localhost:9101/run/start?run=${RUN_NAME}" || true

  # Drive writes from a one-shot raw-bench in "hammer" mode (mode=naive
  # is fine - we don't care about correctness here, only the write
  # rate). Use mode=naive to read from a replica, but we ignore violations.
  docker_run_oneshot raw-bench \
    --mode=naive \
    --rate="${WRITE_RATE}" \
    --duration="${DURATION}" \
    --warmup="${WARMUP_S}s" \
    --workers=32 \
    --out="/perf/results/lag/${RUN_NAME}/raw_bench_during_lag" \
    --primary="postgres://hlsa:hlsa@postgres-primary:5432/hlsa?sslmode=disable" \
    --replica1="postgres://hlsa:hlsa@postgres-replica-1:5432/hlsa?sslmode=disable" \
    --replica2="postgres://hlsa:hlsa@postgres-replica-2:5432/hlsa?sslmode=disable"

  docker compose exec -T lag-sampler wget -qO- "http://localhost:9101/run/stop" || true

  # Snapshot meta + verify samples.csv landed.
  jq -n \
    --arg run "${RUN_NAME}" \
    --arg duration "${DURATION}" \
    --arg rate "${WRITE_RATE}" \
    --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
    '{run: $run, duration: $duration, write_rate: ($rate|tonumber), captured_at: $ts}' \
    > "${RUN_DIR}/meta.json"

  if [ ! -s "${RUN_DIR}/samples.csv" ]; then
    echo "warning: ${RUN_DIR}/samples.csv missing or empty - is lag-sampler healthy?"
  fi
done

echo
echo "Wrote ${RUNS} run(s) under ${LAG_BASE}/"
