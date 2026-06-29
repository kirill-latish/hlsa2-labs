#!/usr/bin/env bash
# inject-5xx-scenarios - drive three scenarios so each edge 5xx class
# shows up in the "5xx by class" panel, then snapshot a metrics file per
# scenario so analyze-5xx can attribute each code:
#
#   502 Bad Gateway      - break ONE backend; the proxy cannot connect
#                          (connection refused) while it is still routing
#   503 Service Unavail. - break ALL backends; no healthy backends left
#   504 Gateway Timeout  - inject latency > proxy timeout on all backends
#
# Inputs (env):
#   LABEL  subdir under perf/results/ (default 5xx)
#   RATE   offered RPS (default 100)
#
# Writes perf/results/<LABEL>/:
#   edge-metrics-start.txt, edge-metrics-502.txt, edge-metrics-503.txt,
#   edge-metrics-504.txt, meta.json
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

_LABEL="${LABEL:-}"; _RATE="${RATE:-}"
if [[ -f .env ]]; then set -a; source .env; set +a; fi

LABEL="${_LABEL:-5xx}"
RATE="${_RATE:-100}"
SCENARIO_S="${SCENARIO_S:-12}"

EDGE="http://localhost:${LAB_EDGE_PORT:-8080}"
LOADGEN="http://localhost:${LAB_LOADGEN_PORT:-8090}"

# Backend admin host ports (for the 504 latency injection).
declare -A BPORT=(
  [backend-1]="${LAB_BACKEND1_PORT:-8081}"
  [backend-2]="${LAB_BACKEND2_PORT:-8082}"
  [backend-3]="${LAB_BACKEND3_PORT:-8083}"
  [backend-4]="${LAB_BACKEND4_PORT:-8084}"
)
ALL=(backend-1 backend-2 backend-3 backend-4)

OUT="perf/results/${LABEL}"
mkdir -p "${OUT}"

backend_admin() { # <backend> <json>
  curl -fsS -X POST -H 'content-type: application/json' -d "$2" \
    "http://localhost:${BPORT[$1]}/admin/config" >/dev/null || true
}

drive_window() { # <label-suffix>
  loadgen_start "${LOADGEN}" "${RATE}" "${SCENARIO_S}" "${LABEL}-$1" "0.1"
  poll_until_done "${LOADGEN}" "${SCENARIO_S}"
}

echo "[inject-5xx] baseline metrics snapshot"
curl -fsS "${EDGE}/metrics" >"${OUT}/edge-metrics-start.txt"

# --- 502: one backend refuses connections -----------------------------
echo "[inject-5xx] 502 scenario: breaking backend-1 (connection refused)"
curl -fsS -X POST "${EDGE}/admin/backend/backend-1/fail" >/dev/null
drive_window "502"
curl -fsS "${EDGE}/metrics" >"${OUT}/edge-metrics-502.txt"
curl -fsS -X POST "${EDGE}/admin/backend/backend-1/restore" >/dev/null
sleep 5  # let it rejoin rotation

# --- 503: all backends down -> no healthy backends --------------------
echo "[inject-5xx] 503 scenario: breaking ALL backends"
for b in "${ALL[@]}"; do curl -fsS -X POST "${EDGE}/admin/backend/${b}/fail" >/dev/null; done
drive_window "503"
curl -fsS "${EDGE}/metrics" >"${OUT}/edge-metrics-503.txt"
for b in "${ALL[@]}"; do curl -fsS -X POST "${EDGE}/admin/backend/${b}/restore" >/dev/null; done
sleep 5

# --- 504: backends slower than the proxy timeout ----------------------
echo "[inject-5xx] 504 scenario: injecting 4000ms latency on all backends"
for b in "${ALL[@]}"; do backend_admin "${b}" '{"extra_latency_ms":4000}'; done
drive_window "504"
curl -fsS "${EDGE}/metrics" >"${OUT}/edge-metrics-504.txt"
for b in "${ALL[@]}"; do backend_admin "${b}" '{"extra_latency_ms":0}'; done

jq -n --arg label "${LABEL}" --argjson rate "${RATE}" --argjson sec "${SCENARIO_S}" \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{label:$label, rate_rps:$rate, scenario_seconds:$sec, captured_at:$captured_at,
    scenarios:["502","503","504"]}' >"${OUT}/meta.json"

echo "[inject-5xx] wrote ${OUT}/   Next: make analyze-5xx LABEL=${LABEL}"
