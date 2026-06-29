#!/usr/bin/env bash
# Shared helpers for the lab 5-1 bench scripts. Source me with:
#   source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Load .env (host port overrides only) so APP/LOADGEN URLs resolve.
if [[ -f "${LAB_ROOT}/.env" ]]; then
  # shellcheck disable=SC1091
  set -a; source "${LAB_ROOT}/.env"; set +a
fi

APP="http://localhost:${LAB_APP_PORT:-8080}"
LOADGEN="http://localhost:${LAB_LOADGEN_PORT:-8090}"

# dur_to_seconds 5m|30s|2h|90 -> seconds.
dur_to_seconds() {
  local d="${1:-0}"
  case "${d}" in
    *h) echo $(( ${d%h} * 3600 )) ;;
    *m) echo $(( ${d%m} * 60 )) ;;
    *s) echo "${d%s}" ;;
    *)  echo "${d}" ;;
  esac
}

# pct_to_num 20pct|20%|20 -> 20
pct_to_num() {
  local p="${1:-0}"
  p="${p%pct}"
  p="${p%\%}"
  echo "${p}"
}

# app_config '<json body>' - POST a partial config patch to the app.
app_config() {
  curl -fsS -X POST "${APP}/admin/config" \
    -H 'content-type: application/json' -d "$1"
}

# poll_until_done <max_wait_s> - block until loadgen reports
# running=false, or until max_wait_s+30 elapses (then it forces a stop).
poll_until_done() {
  local max="${1:-300}"
  local started end
  started="$(date +%s)"
  end=$(( started + max + 30 ))
  while true; do
    local state running
    state="$(curl -fsS "${LOADGEN}/state" 2>/dev/null || echo '{"running":false}')"
    running="$(echo "${state}" | jq -r '.running // false')"
    if [[ "${running}" != "true" ]]; then
      echo "[poll] loadgen completed."
      return 0
    fi
    if [[ "$(date +%s)" -gt "${end}" ]]; then
      echo "[poll] loadgen still running after ${max}s + 30s grace; stopping it."
      curl -fsS -X POST "${LOADGEN}/stop" >/dev/null || true
      return 1
    fi
    sleep 5
  done
}

# snapshot_app_metrics <path> - write the app /metrics dump to a file.
snapshot_app_metrics() {
  curl -fsS "${APP}/metrics" >"$1"
}
