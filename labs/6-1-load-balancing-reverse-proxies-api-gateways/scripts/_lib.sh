#!/usr/bin/env bash
# Shared helpers for the lab 6-1 bench scripts. Source me with:
#   source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

# to_seconds <dur> - accept 5m / 30s / 180 and echo whole seconds.
to_seconds() {
  local d="${1:-0}"
  case "$d" in
    *m) echo $(( ${d%m} * 60 )) ;;
    *s) echo "${d%s}" ;;
    *)  echo "$d" ;;
  esac
}

# poll_until_done <loadgen-url> <max_wait_s>
# Blocks until loadgen reports running=false, or until max_wait_s+30 elapses.
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
      echo "[poll] loadgen still running after ${max}s + 30s grace; stopping it."
      curl -fsS -X POST "${lg}/stop" >/dev/null || true
      return 1
    fi
    sleep 2
  done
}

# loadgen_start <loadgen-url> <rate> <duration_s> <label> <slow_ratio>
loadgen_start() {
  local lg="$1" rate="$2" dur="$3" label="$4" slow="${5:-0.1}"
  curl -fsS -X POST -H 'content-type: application/json' \
    -d "$(jq -n --argjson r "${rate}" --argjson d "${dur}" --arg l "${label}" --argjson s "${slow}" \
            '{rate_rps:$r,duration_s:$d,label:$l,slow_ratio:$s}')" \
    "${lg}/start" >/dev/null
}

# edge_set_config <edge-url> <json-body> - POST partial config to the edge.
edge_set_config() {
  local edge="$1" body="$2"
  curl -fsS -X POST -H 'content-type: application/json' -d "${body}" "${edge}/admin/config"
}

# edge_status <edge-url>
edge_status() { curl -fsS "$1/admin/status"; }
