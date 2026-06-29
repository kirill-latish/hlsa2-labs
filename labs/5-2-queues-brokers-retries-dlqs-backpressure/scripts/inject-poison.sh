#!/usr/bin/env bash
# inject-poison - drive a baseline-rate run that injects COUNT poison
# messages near the start, capturing the consumer collapse (under
# unbounded retries) or the bounded-retry+DLQ recovery.
#
#   COUNT    number of poison messages to inject (default 1)
#   LABEL    perf/results/<LABEL>/ subdir (default poison)
#   DURATION run length, e.g. 3m / 180s (default 3m)
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"
load_env

COUNT="${COUNT:-1}"
LABEL="${LABEL:-poison}"
DURATION="${DURATION:-3m}"
RATE="${RATE:-240}"
DUR_S="$(to_seconds "${DURATION}")"

OUT="perf/results/${LABEL}/run1"
echo "[inject-poison] COUNT=${COUNT} LABEL=${LABEL} duration=${DUR_S}s rate=${RATE}"
drive_run "${OUT}" "${LABEL}" "${RATE}" "${DUR_S}" "${COUNT}" 0 0 1 false

echo
echo "Done. Next:"
echo "  make analyze-poison LABEL=${LABEL}"
