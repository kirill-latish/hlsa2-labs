#!/usr/bin/env bash
# Shared helpers for the lab 4-3 bench scripts. Mirrors lab 4-2.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RESULTS_ROOT="${LAB_ROOT}/perf/results"
SCRIPTS_ROOT="${LAB_ROOT}/scripts"

mkdir -p "${RESULTS_ROOT}"

NETWORK="hlsa2-lab43_default"

# docker_run_oneshot builds (once) and runs a Go binary on the lab's
# docker network. Used by every bench-* helper. Honours the
# `.candidate` written by `make apply-fix` so SHARD_KEY=fixed resolves
# correctly inside the one-shot container.
docker_run_oneshot() {
  local bin="$1"; shift
  local image="lab43-oneshot-${bin}"

  if ! docker image inspect "${image}" >/dev/null 2>&1; then
    docker build --build-arg "BIN=${bin}" -t "${image}" "${LAB_ROOT}" >/dev/null
  fi

  local extra=()
  if [[ -f "${LAB_ROOT}/.candidate" ]]; then
    extra+=( -v "${LAB_ROOT}/.candidate:/lab/.candidate:ro" -e "FIXED_FALLBACK=$(cat "${LAB_ROOT}/.candidate")" )
  fi

  docker run --rm \
    --network "${NETWORK}" \
    -v "${RESULTS_ROOT}:/perf/results" \
    -e POSTGRES_DSN="postgres://hlsa:hlsa@postgres:5432/hlsa?sslmode=disable" \
    -e ES_URL="http://elasticsearch:9200" \
    -e ES_CONSUMER_URL="http://es-consumer:9000" \
    -e MONGO_HOSTS="mongos-1:27017,mongos-2:27017" \
    -e MONGO_DB="lab43" \
    -e SHARD_HOSTS="mongo-shard-1:27017,mongo-shard-2:27017,mongo-shard-3:27017" \
    -e LOADGEN_URL="http://loadgen:9000" \
    -e REDPANDA_BROKERS="redpanda:9092" \
    -e FAULT_INJECTOR_URL="http://fault-injector:9000" \
    ${extra[@]+"${extra[@]}"} \
    "${image}" "$@"
}

log_step() {
  printf '\n=== %s ===\n' "$*"
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo >&2 "missing dependency: $1"; exit 1; }
}

require_cmd docker
require_cmd jq
