#!/usr/bin/env bash
# run-bench-faults - inject a mix of transient + permanent failures
# across the workload and capture how retries and the DLQ respond.
# Pair with `make apply-fix CANDIDATE=classify-failures` first.
#
#   TRANSIENT_RATE  e.g. 10pct / 0.10 (default 10pct)
#   PERMANENT_RATE  e.g. 2pct  / 0.02 (default 2pct)
#   DURATION        run length, e.g. 5m (default 5m)
#   LABEL           perf/results/<LABEL>/ subdir (default faults)
#   RATE            producer rate msgs/s (default 240)
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"
load_env

TRANSIENT_RATE="${TRANSIENT_RATE:-10pct}"
PERMANENT_RATE="${PERMANENT_RATE:-2pct}"
DURATION="${DURATION:-5m}"
LABEL="${LABEL:-faults}"
RATE="${RATE:-240}"

TR_FRAC="$(pct_to_frac "${TRANSIENT_RATE}")"
PR_FRAC="$(pct_to_frac "${PERMANENT_RATE}")"
DUR_S="$(to_seconds "${DURATION}")"

OUT="perf/results/${LABEL}/run1"
echo "[bench-faults] transient=${TRANSIENT_RATE}(${TR_FRAC}) permanent=${PERMANENT_RATE}(${PR_FRAC}) duration=${DUR_S}s"
drive_run "${OUT}" "${LABEL}" "${RATE}" "${DUR_S}" 0 "${TR_FRAC}" "${PR_FRAC}" 1 false

echo
echo "Done. Next: make analyze-faults LABEL=${LABEL}"
