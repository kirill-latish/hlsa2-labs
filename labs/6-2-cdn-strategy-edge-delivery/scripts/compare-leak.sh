#!/usr/bin/env bash
# compare-leak - side-by-side cross-user leak counts before and after the
# personalized-content fix. Reads the probe summaries written by
# probe-cross-user.sh.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"

BEFORE="${BEFORE:?BEFORE=<label> is required (e.g. leak-before)}"
AFTER="${AFTER:?AFTER=<label> is required (e.g. leak-after)}"

A="perf/results/${BEFORE}/summary.json"
B="perf/results/${AFTER}/summary.json"
[[ -f "${A}" ]] || { echo "no such file: ${A} (run make probe-cross-user LABEL=${BEFORE})" >&2; exit 2; }
[[ -f "${B}" ]] || { echo "no such file: ${B} (run make probe-cross-user LABEL=${AFTER})" >&2; exit 2; }

a_leak="$(jq -r '.leaked' "${A}")"
a_req="$(jq -r '.requests' "${A}")"
b_leak="$(jq -r '.leaked' "${B}")"
b_req="$(jq -r '.requests' "${B}")"

echo "# compare-leak: ${BEFORE} -> ${AFTER}"
echo
echo "| metric | ${BEFORE} | ${AFTER} |"
echo "|--------|------:|------:|"
echo "| leaked responses | ${a_leak} | ${b_leak} |"
echo "| probe requests | ${a_req} | ${b_req} |"
echo
if [[ "${b_leak}" -eq 0 && "${a_leak}" -gt 0 ]]; then
  echo "RESULT: leak ELIMINATED (${a_leak} -> 0)."
elif [[ "${b_leak}" -gt 0 ]]; then
  echo "RESULT: leak STILL PRESENT after the fix (${b_leak}). Investigate the personalized_mode config."
else
  echo "RESULT: no leak observed in either condition."
fi
echo
echo "Mechanism: the shared edge cache serves anything stored under a key to"
echo "EVERY requester with that key. Personalized content therefore needs identity"
echo "in the key (per-user) or must be marked private/no-store. Trade-off: per-user"
echo "keys eliminate cross-user sharing entirely (no offload for that content)."
