#!/usr/bin/env bash
# Run the read-after-write benchmark for one MODE.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

MODE="${MODE:-naive}"
RATE="${RATE:-200}"
DURATION="${DURATION:-30s}"
WARMUP_S="${WARMUP_S:-5}"

RAW_DIR="${RESULTS_ROOT}/raw/${MODE}"
mkdir -p "${RAW_DIR}"

log_step "raw bench mode=${MODE} rate=${RATE}/s duration=${DURATION}"

docker_run_oneshot raw-bench \
  --mode="${MODE}" \
  --rate="${RATE}" \
  --duration="${DURATION}" \
  --warmup="${WARMUP_S}s" \
  --workers=32 \
  --out="/perf/results/raw/${MODE}"

echo
if [ -f "${RAW_DIR}/summary.json" ]; then
  echo "Summary:"
  jq '.' "${RAW_DIR}/summary.json"
fi
