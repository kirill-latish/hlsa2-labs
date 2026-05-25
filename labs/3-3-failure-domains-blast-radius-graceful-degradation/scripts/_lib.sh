#!/usr/bin/env bash
# Shared helpers for the bench scripts. Source me with:
#   source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

# poll_until_done <loadgen-url> <max_wait_s>
# Blocks until loadgen reports running=false, or until max_wait_s+30 elapses.
poll_until_done() {
  local lg="$1"
  local max="${2:-300}"
  local started end
  started="$(date +%s)"
  end=$(( started + max + 30 ))
  while true; do
    local state
    state="$(curl -fsS "${lg}/state" 2>/dev/null || echo '{"running":false}')"
    local running
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
    sleep 5
  done
}

# bool_to_json on|off → true|false
_bool_to_json() {
  case "${1:-off}" in
    on|true|1|yes) echo "true" ;;
    *) echo "false" ;;
  esac
}

# push_gateway_controls <gateway-url> <bulkhead> <cb> <fallback> <retry> <shed>
# Flips the gateway runtime flags via /admin/config so each labelled
# bench run uses the controls passed on the make line. Safe to call
# even when all values are 'off'.
push_gateway_controls() {
  local gw="$1"
  local bh cb fb rb ls
  bh="$(_bool_to_json "${2:-off}")"
  cb="$(_bool_to_json "${3:-off}")"
  fb="$(_bool_to_json "${4:-off}")"
  rb="$(_bool_to_json "${5:-off}")"
  ls="$(_bool_to_json "${6:-off}")"
  curl -fsS -X POST "${gw}/admin/config" \
    -H 'content-type: application/json' \
    -d "{\"bulkhead\":${bh},\"circuit_breaker\":${cb},\"fallback\":${fb},\"retry_budget\":${rb},\"load_shed\":${ls}}" >/dev/null
}
