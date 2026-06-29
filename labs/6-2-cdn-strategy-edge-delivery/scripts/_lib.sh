#!/usr/bin/env bash
# Shared helpers for the lab-6-2 bench/probe scripts. Source me with:
#   source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
#
# All scripts run on the HOST and talk to the containers via the
# published host ports (overridable in .env).

# Load .env so the LAB_*_PORT overrides apply, without clobbering values
# the caller already exported.
_load_env() {
  if [[ -f .env ]]; then
    # shellcheck disable=SC1091
    set -a; source .env; set +a
  fi
}
_load_env

# Host-facing base URLs for every node.
ORIGIN_URL="http://localhost:${LAB_ORIGIN_PORT:-8088}"
SHIELD_URL="http://localhost:${LAB_SHIELD_PORT:-8086}"
POP1_URL="http://localhost:${LAB_POP1_PORT:-8081}"
POP2_URL="http://localhost:${LAB_POP2_PORT:-8082}"
POP3_URL="http://localhost:${LAB_POP3_PORT:-8083}"
LOADGEN_URL="http://localhost:${LAB_LOADGEN_PORT:-8090}"

POP_URLS=("${POP1_URL}" "${POP2_URL}" "${POP3_URL}")

# parse_duration "5m"|"30s"|"180" -> seconds (integer).
parse_duration() {
  local d="${1:-0}"
  case "${d}" in
    *m) echo $(( ${d%m} * 60 )) ;;
    *s) echo "${d%s}" ;;
    *)  echo "${d}" ;;
  esac
}

# poll_until_done <loadgen-url> <max_wait_s> - blocks until loadgen
# reports running=false, or until max_wait_s + 30 grace elapses.
poll_until_done() {
  local lg="$1"
  local max="${2:-300}"
  local started end
  started="$(date +%s)"
  end=$(( started + max + 30 ))
  while true; do
    local state running
    state="$(curl -fsS "${lg}/state" 2>/dev/null || echo '{"running":false}')"
    running="$(echo "${state}" | jq -r '.running // false')"
    if [[ "${running}" != "true" ]]; then
      echo "[poll] loadgen completed."
      return 0
    fi
    if [[ "$(date +%s)" -gt "${end}" ]]; then
      echo "[poll] loadgen still running after ${max}s + grace; stopping it."
      curl -fsS -X POST "${lg}/stop" >/dev/null || true
      return 1
    fi
    sleep 5
  done
}

# push_config_pops '<json>' - POST a partial config to all three PoPs.
push_config_pops() {
  local json="$1"
  local u
  for u in "${POP_URLS[@]}"; do
    curl -fsS -X POST -H 'content-type: application/json' -d "${json}" "${u}/admin/config" >/dev/null
  done
}

# push_config_shield '<json>'
push_config_shield() {
  curl -fsS -X POST -H 'content-type: application/json' -d "$1" "${SHIELD_URL}/admin/config" >/dev/null
}

# push_config_origin '<json>'
push_config_origin() {
  curl -fsS -X POST -H 'content-type: application/json' -d "$1" "${ORIGIN_URL}/admin/config" >/dev/null
}

# flush_edge - drop every cache entry on all PoPs + the shield so a run
# starts cold.
flush_edge() {
  local u
  for u in "${POP_URLS[@]}" "${SHIELD_URL}"; do
    curl -fsS -X POST "${u}/admin/flush" >/dev/null || true
  done
}

# snapshot_metrics <dir> <suffix> - capture /metrics from every node into
# <dir>/<node>-metrics-<suffix>.txt. Used before/after a run so analyzers
# can compute per-run deltas instead of cumulative-since-boot totals.
snapshot_metrics() {
  local dir="$1" suffix="$2"
  mkdir -p "${dir}"
  curl -fsS "${POP1_URL}/metrics"   >"${dir}/pop-1-metrics-${suffix}.txt"   || true
  curl -fsS "${POP2_URL}/metrics"   >"${dir}/pop-2-metrics-${suffix}.txt"   || true
  curl -fsS "${POP3_URL}/metrics"   >"${dir}/pop-3-metrics-${suffix}.txt"   || true
  curl -fsS "${SHIELD_URL}/metrics" >"${dir}/shield-metrics-${suffix}.txt"  || true
  curl -fsS "${ORIGIN_URL}/metrics" >"${dir}/origin-metrics-${suffix}.txt"  || true
  curl -fsS "${LOADGEN_URL}/metrics">"${dir}/loadgen-metrics-${suffix}.txt" || true
}

# drive_load <rate> <duration_s> <label> - start loadgen and block until
# the run completes.
drive_load() {
  local rate="$1" dur="$2" label="$3"
  curl -fsS -X POST -H 'content-type: application/json' \
    -d "$(jq -n --argjson r "${rate}" --argjson d "${dur}" --arg l "${label}" \
            '{rate_rps:$r,duration_s:$d,label:$l}')" \
    "${LOADGEN_URL}/start" >/dev/null
  poll_until_done "${LOADGEN_URL}" "${dur}"
}
