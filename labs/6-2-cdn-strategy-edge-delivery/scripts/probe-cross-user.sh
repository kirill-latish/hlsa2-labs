#!/usr/bin/env bash
# probe-cross-user - drive the loadgen's cross-user leak probe: send many
# requests to the personalized route as DIFFERENT users (each its own uid
# cookie) against a single PoP, and count how many come back personalized
# for the WRONG user. A nonzero leak count is a real data breach.
#
# Inputs (env): LABEL, USERS, REQUESTS.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

LABEL="${LABEL:-leak}"
USERS="${USERS:-20}"
REQUESTS="${REQUESTS:-200}"

OUT_DIR="perf/results/${LABEL}"
mkdir -p "${OUT_DIR}"

echo "[probe-cross-user] LABEL=${LABEL} users=${USERS} requests=${REQUESTS}"
RESULT="$(curl -fsS -X POST -H 'content-type: application/json' \
  -d "$(jq -n --argjson u "${USERS}" --argjson r "${REQUESTS}" --arg l "${LABEL}" \
          '{users:$u, requests:$r, label:$l}')" \
  "${LOADGEN_URL}/probe")"

echo "${RESULT}" | jq '.' | tee "${OUT_DIR}/summary.json"

LEAKED="$(echo "${RESULT}" | jq -r '.leaked')"
if [[ "${LEAKED}" -gt 0 ]]; then
  echo "[probe-cross-user] LEAK REPRODUCED: ${LEAKED} responses served for the wrong user."
else
  echo "[probe-cross-user] clean: 0 cross-user leaks."
fi
echo "[probe-cross-user] wrote ${OUT_DIR}/summary.json"
