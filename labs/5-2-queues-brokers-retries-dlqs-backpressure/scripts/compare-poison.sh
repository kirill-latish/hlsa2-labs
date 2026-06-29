#!/usr/bin/env bash
# compare-poison - side-by-side BEFORE vs AFTER for the poison
# experiment (unbounded-retry collapse vs bounded-retry+DLQ recovery).
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"

BEFORE="${BEFORE:?BEFORE=<label> is required (e.g. poison-baseline)}"
AFTER="${AFTER:?AFTER=<label> is required (e.g. poison-after)}"

A="perf/results/${BEFORE}"
B="perf/results/${AFTER}"
[[ -d "${A}" ]] || { echo "no such dir: ${A}" >&2; exit 2; }
[[ -d "${B}" ]] || { echo "no such dir: ${B}" >&2; exit 2; }

echo "[compare-poison] BEFORE=${BEFORE} AFTER=${AFTER}"
python3 scripts/analyze-compare.py "${A}" "${B}"
