#!/usr/bin/env bash
# run-bench-baseline - drive the pipeline at a moderate rate (~80% of
# consumer capacity) for several runs, capturing lag (count AND time),
# throughput, processing latency, retry, and DLQ.
#
#   RUNS     runs to execute (default 3)
#   LABEL    perf/results/<LABEL>/ subdir (default baseline)
#   DURATION run length per run, e.g. 5m / 300s (default 5m)
#   RATE     producer rate in msgs/s (default 240)
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"
load_env

RUNS="${RUNS:-3}"
LABEL="${LABEL:-baseline}"
DURATION="${DURATION:-5m}"
RATE="${RATE:-240}"
DUR_S="$(to_seconds "${DURATION}")"

OUT_BASE="perf/results/${LABEL}"
mkdir -p "${OUT_BASE}"

for i in $(seq 1 "${RUNS}"); do
  echo
  echo "================================================================="
  echo "[bench-baseline] LABEL=${LABEL} run=${i}/${RUNS} rate=${RATE} duration=${DUR_S}s"
  echo "================================================================="
  drive_run "${OUT_BASE}/run${i}" "${LABEL}-run${i}" "${RATE}" "${DUR_S}" 0 0 0 1 false
done

echo
echo "Done. Next: make analyze-baseline LABEL=${LABEL}"
