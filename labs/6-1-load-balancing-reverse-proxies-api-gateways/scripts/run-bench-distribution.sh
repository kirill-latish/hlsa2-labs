#!/usr/bin/env bash
# run-bench-distribution - set the balancing algorithm, drive uneven-cost
# traffic, and record the per-backend request distribution.
#
# Inputs (env):
#   ALGO       round-robin|least-conn (default round-robin)
#   DURATION   accepts 3m/30s/180 (default 3m)
#   LABEL      subdir under perf/results/distribution/ (default dist-${ALGO})
#   RATE       offered RPS (default 200)
#   SLOW_RATIO uneven-cost fraction (default 0.3)
#
# Writes perf/results/distribution/<LABEL>/:
#   status-start.json / status-end.json   edge /admin/status (count delta = run traffic)
#   edge-metrics.txt                       edge /metrics
#   summary.json                           loadgen /summary
#   meta.json
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

_ALGO="${ALGO:-}"; _DURATION="${DURATION:-}"; _LABEL="${LABEL:-}"
_RATE="${RATE:-}"; _SLOW="${SLOW_RATIO:-}"
if [[ -f .env ]]; then set -a; source .env; set +a; fi

ALGO="${_ALGO:-round-robin}"
DURATION="${_DURATION:-3m}"
LABEL="${_LABEL:-dist-${ALGO}}"
RATE="${_RATE:-200}"
SLOW_RATIO="${_SLOW:-0.3}"
DURATION_S="$(to_seconds "${DURATION}")"

case "${ALGO}" in round-robin|least-conn) ;; *)
  echo "ERROR: ALGO must be round-robin|least-conn" >&2; exit 2 ;;
esac

EDGE="http://localhost:${LAB_EDGE_PORT:-8080}"
LOADGEN="http://localhost:${LAB_LOADGEN_PORT:-8090}"
OUT="perf/results/distribution/${LABEL}"
mkdir -p "${OUT}"

echo "[run-bench-distribution] setting algo=${ALGO}"
edge_set_config "${EDGE}" "$(jq -n --arg a "${ALGO}" '{algo:$a}')"
echo

echo "[run-bench-distribution] LABEL=${LABEL} rate=${RATE} duration=${DURATION_S}s slow_ratio=${SLOW_RATIO}"
edge_status "${EDGE}" >"${OUT}/status-start.json"
loadgen_start "${LOADGEN}" "${RATE}" "${DURATION_S}" "${LABEL}" "${SLOW_RATIO}"
poll_until_done "${LOADGEN}" "${DURATION_S}"

edge_status "${EDGE}" >"${OUT}/status-end.json"
curl -fsS "${LOADGEN}/summary?label=${LABEL}" >"${OUT}/summary.json"
curl -fsS "${EDGE}/metrics" >"${OUT}/edge-metrics.txt"
jq -n \
  --arg label "${LABEL}" --arg algo "${ALGO}" \
  --argjson rate "${RATE}" --argjson dur "${DURATION_S}" --argjson slow "${SLOW_RATIO}" \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{label:$label, algo:$algo, rate_rps:$rate, duration_s:$dur, slow_ratio:$slow, captured_at:$captured_at}' \
  >"${OUT}/meta.json"

echo "[run-bench-distribution] wrote ${OUT}/"
echo
python3 scripts/compare-distribution.py "${OUT}" || true
