#!/usr/bin/env bash
# Shared helpers for the lab 5-3 scripts. Source me with:
#   source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
set -uo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RESULTS_ROOT="${LAB_ROOT}/perf/results"
SCRIPTS_ROOT="${LAB_ROOT}/scripts"
mkdir -p "${RESULTS_ROOT}"
# Run from the lab root so `docker compose` always finds the compose file.
cd "${LAB_ROOT}"

# Load host-side port overrides (defaults match .env.example).
if [[ -f "${LAB_ROOT}/.env" ]]; then
  set -a; source "${LAB_ROOT}/.env"; set +a
fi

PRODUCER="http://localhost:${LAB_PRODUCER_PORT:-8080}"
CONSUMER1="http://localhost:${LAB_CONSUMER1_PORT:-9103}"
CONSUMER2="http://localhost:${LAB_CONSUMER2_PORT:-9113}"
CONSUMER3="http://localhost:${LAB_CONSUMER3_PORT:-9123}"
RELAY="http://localhost:${LAB_OUTBOX_RELAY_PORT:-9102}"
PROM="http://localhost:${LAB_PROMETHEUS_PORT:-9090}"
CONSUMERS=("${CONSUMER1}" "${CONSUMER2}" "${CONSUMER3}")
TOPIC="${EVENTS_TOPIC:-order-events}"
GROUP="${CONSUMER_GROUP:-lab53-consumers}"
PARTITIONS="${REDPANDA_PARTITIONS:-6}"

log_step() { printf '\n=== %s ===\n' "$*"; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo >&2 "missing dependency: $1"; exit 1; }
}

# to_seconds 5m | 90s | 180  -> integer seconds
to_seconds() {
  local v="${1:-0}"
  case "${v}" in
    *m) echo $(( ${v%m} * 60 )) ;;
    *s) echo "${v%s}" ;;
    *)  echo "${v}" ;;
  esac
}

# psql_q "<SQL>" -> tab/atom output from the lab Postgres.
psql_q() {
  docker compose exec -T postgres psql -U lab53 -d lab53 -tAc "$1" 2>/dev/null | tr -d '[:space:]'
}

# psql_exec "<SQL>" -> run a statement, ignore output.
psql_exec() {
  docker compose exec -T postgres psql -U lab53 -d lab53 -c "$1" >/dev/null 2>&1
}

# rpk <args> -> run rpk inside the redpanda container.
rpk() { docker compose exec -T redpanda rpk "$@"; }

# Set all three consumers to a given consumer mode (idempotent/naive).
set_consumer_mode() {
  local mode="$1"
  for c in "${CONSUMERS[@]}"; do
    curl -fsS -X POST "${c}/admin/mode" -H 'content-type: application/json' \
      -d "{\"mode\":\"${mode}\"}" >/dev/null || true
  done
}

# Set the replay mode on all three consumers.
set_replay_mode() {
  local mode="$1"
  for c in "${CONSUMERS[@]}"; do
    curl -fsS -X POST "${c}/admin/replay-mode" -H 'content-type: application/json' \
      -d "{\"mode\":\"${mode}\"}" >/dev/null || true
  done
}

# Flip the producer's runtime config.
producer_config() {
  curl -fsS -X POST "${PRODUCER}/admin/config" -H 'content-type: application/json' -d "$1" >/dev/null
}

# Drive the producer for duration_s seconds at rate_eps, then wait for
# it to finish.
producer_run() {
  local rate="$1" dur="$2" label="$3"
  curl -fsS -X POST "${PRODUCER}/start" -H 'content-type: application/json' \
    -d "$(printf '{"rate_eps":%d,"duration_s":%d,"label":"%s"}' "${rate}" "${dur}" "${label}")" >/dev/null
  # Poll producer /state until running=false (+grace).
  local end=$(( $(date +%s) + dur + 30 ))
  while true; do
    local running
    running="$(curl -fsS "${PRODUCER}/state" 2>/dev/null | grep -o '"running":[a-z]*' | head -1 | cut -d: -f2)"
    [[ "${running}" != "true" ]] && break
    [[ "$(date +%s)" -gt "${end}" ]] && { curl -fsS -X POST "${PRODUCER}/stop" >/dev/null || true; break; }
    sleep 2
  done
}

# Wait until the consumer lag count gauge reads 0 (or timeout).
wait_consumer_drained() {
  local timeout="${1:-60}"
  local end=$(( $(date +%s) + timeout ))
  while true; do
    local lag
    lag="$(promget 'lab53:consumer_lag_count')"
    [[ -z "${lag}" || "${lag%.*}" == "0" ]] && return 0
    [[ "$(date +%s)" -gt "${end}" ]] && return 0
    sleep 2
  done
}

# Wait until the outbox backlog reads 0 (or timeout).
wait_outbox_drained() {
  local timeout="${1:-60}"
  local end=$(( $(date +%s) + timeout ))
  while true; do
    local b
    b="$(psql_q 'SELECT count(*) FROM events_outbox WHERE published_at IS NULL')"
    [[ "${b}" == "0" || -z "${b}" ]] && return 0
    [[ "$(date +%s)" -gt "${end}" ]] && return 0
    sleep 2
  done
}

# promget '<instant query>' -> scalar value (first sample) from Prometheus.
promget() {
  curl -fsS --get "${PROM}/api/v1/query" --data-urlencode "query=$1" 2>/dev/null \
    | python3 -c 'import sys,json
try:
    d=json.load(sys.stdin)["data"]["result"]
    print(d[0]["value"][1] if d else "")
except Exception:
    print("")'
}

require_cmd docker
require_cmd curl
require_cmd python3
