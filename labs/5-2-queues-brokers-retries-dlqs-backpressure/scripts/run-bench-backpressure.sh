#!/usr/bin/env bash
# run-bench-backpressure - drive sustained overload (e.g. 2x consumer
# capacity) for a long window and capture whether lag stabilizes
# (backpressure honored) or grows unbounded (backpressure ignored).
# Pair with `make apply-fix CANDIDATE=backpressure-signal` first.
#
#   RATE     overload, e.g. 2x (multiple of consumer capacity) or plain rps (default 2x)
#   DURATION run length, e.g. 10m (default 10m)
#   LABEL    perf/results/<LABEL>/ subdir (default backpressure)
#   CAPACITY aggregate consumer capacity msgs/s used to resolve "Nx" (default 300)
#   BACKPRESSURE  true|false - whether the producer honors broker
#                 backpressure (default true; set false to demo the
#                 unbounded-growth failure mode)
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"
load_env

RATE="${RATE:-2x}"
DURATION="${DURATION:-10m}"
LABEL="${LABEL:-backpressure}"
CAPACITY="${CAPACITY:-300}"
BACKPRESSURE="${BACKPRESSURE:-true}"

RPS="$(rate_to_rps "${RATE}" "${CAPACITY}")"
DUR_S="$(to_seconds "${DURATION}")"

OUT="perf/results/${LABEL}/run1"
echo "[bench-backpressure] RATE=${RATE} -> ${RPS} msgs/s, duration=${DUR_S}s, backpressure=${BACKPRESSURE}"
drive_run "${OUT}" "${LABEL}" "${RPS}" "${DUR_S}" 0 0 0 1 "${BACKPRESSURE}"

echo
echo "Done. Next: make analyze-backpressure LABEL=${LABEL}"
