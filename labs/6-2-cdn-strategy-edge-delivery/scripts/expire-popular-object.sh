#!/usr/bin/env bash
# expire-popular-object - reproduce the distributed thundering herd. Warm
# the hot object on every PoP, snapshot the origin, then expire (purge)
# the object across ALL PoPs at once and immediately fire a concurrent
# burst for it at every PoP. The origin fan-in (how many fetches the
# origin sees for that one object) is the thing we measure:
#   shield off -> fan-in ~ number of PoPs
#   shield on  -> shield collapses them to ~one origin fetch
#
# Inputs (env): LABEL, BURST (concurrent reqs per PoP).
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

LABEL="${LABEL:-fanin}"
BURST="${BURST:-40}"
OBJ_ID="s0"
OBJ_PATH="/obj/${OBJ_ID}"

OUT_DIR="perf/results/${LABEL}"
mkdir -p "${OUT_DIR}"

echo "[expire-popular-object] LABEL=${LABEL} object=${OBJ_PATH} burst=${BURST}/PoP"

# 1) Warm the object on every PoP so it is cached everywhere.
for u in "${POP_URLS[@]}"; do
  curl -fsS -o /dev/null "${u}${OBJ_PATH}" || true
done
sleep 1

# 2) Snapshot the origin AFTER warming (so warm fetches are excluded).
snapshot_metrics "${OUT_DIR}" before

# 3) Expire the object across all PoPs simultaneously.
for u in "${POP_URLS[@]}"; do
  curl -fsS -X POST -H 'content-type: application/json' \
    -d "$(jq -n --arg p "${OBJ_PATH}" '{path:$p}')" "${u}/admin/purge" >/dev/null || true
done

# 4) Immediately fire a concurrent burst at every PoP for the now-cold
#    object. All PoPs miss at once -> the herd.
for u in "${POP_URLS[@]}"; do
  for _ in $(seq 1 "${BURST}"); do
    curl -fsS -o /dev/null "${u}${OBJ_PATH}" &
  done
done
wait
sleep 2

# 5) Snapshot the origin AFTER the burst.
snapshot_metrics "${OUT_DIR}" after

jq -n --arg label "${LABEL}" --arg object "${OBJ_ID}" --argjson burst "${BURST}" \
  --argjson pops "${#POP_URLS[@]}" --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{label:$label, object:$object, burst_per_pop:$burst, pops:$pops, captured_at:$captured_at}' \
  >"${OUT_DIR}/meta.json"

echo "[expire-popular-object] wrote ${OUT_DIR}/. Next: make analyze-fanin LABEL=${LABEL}"
