#!/usr/bin/env bash
# compare - side-by-side BEFORE vs AFTER decision using the 2-sigma rule.
# Enforces that both labels share the same active fault spec so the
# improvement claim is honest.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"

BEFORE="${BEFORE:?BEFORE=<label> is required (e.g. faulted-before)}"
AFTER="${AFTER:?AFTER=<label> is required (e.g. faulted-after)}"

A="perf/results/${BEFORE}"
B="perf/results/${AFTER}"
[[ -d "${A}" ]] || { echo "no such dir: ${A}" >&2; exit 2; }
[[ -d "${B}" ]] || { echo "no such dir: ${B}" >&2; exit 2; }

# Pull active_fault from each side's run1 meta.json. Both must match.
get_fault() {
  local dir="$1"
  local meta
  meta="$(ls "${dir}"/*/meta.json 2>/dev/null | head -1 || true)"
  if [[ -z "${meta}" ]]; then
    echo '{}'
    return
  fi
  jq -c '.active_fault // {}' "${meta}"
}

FAULT_A="$(get_fault "${A}")"
FAULT_B="$(get_fault "${B}")"

if [[ "${FAULT_A}" != "${FAULT_B}" ]]; then
  echo "FAIL: BEFORE and AFTER were run under different faults." >&2
  echo "  BEFORE fault: ${FAULT_A}" >&2
  echo "  AFTER  fault: ${FAULT_B}" >&2
  echo "Identical-fault comparison is required to make a fair claim." >&2
  exit 3
fi

echo "[compare] identical fault confirmed: ${FAULT_A}"
echo "[compare] running analyze-compare.py ${A} ${B}"
python3 scripts/analyze-compare.py "${A}" "${B}"
