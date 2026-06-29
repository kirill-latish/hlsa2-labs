#!/usr/bin/env bash
# compare-stampede - side-by-side fan-in ratio BEFORE vs AFTER the fix.
# Both labels must have been analysed first (make analyze-stampede).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
cd "${LAB_ROOT}"

BEFORE="${BEFORE:?BEFORE=<label> is required (e.g. stampede-baseline)}"
AFTER="${AFTER:?AFTER=<label> is required (e.g. stampede-after)}"

python3 scripts/compare-stampede.py "perf/results/${BEFORE}" "perf/results/${AFTER}"
