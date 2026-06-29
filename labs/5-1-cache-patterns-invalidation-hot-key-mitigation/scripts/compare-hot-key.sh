#!/usr/bin/env bash
# compare-hot-key - per-shard ops imbalance BEFORE vs AFTER local-LRU.
# Defaults to hot-baseline vs hot-after when BEFORE/AFTER are omitted.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
cd "${LAB_ROOT}"

BEFORE="${BEFORE:-hot-baseline}"
AFTER="${AFTER:-hot-after}"

python3 scripts/compare-hot-key.py "perf/results/${BEFORE}" "perf/results/${AFTER}"
