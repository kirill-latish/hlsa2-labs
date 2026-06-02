#!/usr/bin/env bash
# Shared helpers for the lab 4-2 bench scripts.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RESULTS_ROOT="${LAB_ROOT}/perf/results"
SCRIPTS_ROOT="${LAB_ROOT}/scripts"

mkdir -p "${RESULTS_ROOT}"

# Run a Go binary inside a one-shot container on the lab's network.
# Args: bin_name [--flag=val ...]. Mounts perf/results so the binary
# can write summary.json directly to the host.
docker_run_go() {
  local bin="$1"; shift
  docker compose run --rm \
    -v "${LAB_ROOT}/perf/results:/perf/results" \
    --workdir /app \
    --use-aliases \
    --entrypoint /app/app \
    --no-deps \
    --build \
    "${bin}" "$@"
}

# Variant that picks an existing service on the network rather than
# building a one-shot. Used when the binary lives in cmd/ but isn't a
# compose service. We instead `docker run` directly on the network.
docker_run_oneshot() {
  local bin="$1"; shift
  local image="lab42-oneshot-${bin}"
  if ! docker image inspect "${image}" >/dev/null 2>&1; then
    docker build --build-arg "BIN=${bin}" -t "${image}" "${LAB_ROOT}" >/dev/null
  fi
  docker run --rm \
    --network "hlsa2-lab42_default" \
    -v "${LAB_ROOT}/perf/results:/perf/results" \
    -e PRIMARY_DSN="postgres://hlsa:hlsa@postgres-primary:5432/hlsa?sslmode=disable" \
    -e REPLICA1_DSN="postgres://hlsa:hlsa@postgres-replica-1:5432/hlsa?sslmode=disable" \
    -e REPLICA2_DSN="postgres://hlsa:hlsa@postgres-replica-2:5432/hlsa?sslmode=disable" \
    -e ORCHESTRATOR_URL="http://orchestrator:8080" \
    -e REDPANDA_BROKERS="redpanda:9092" \
    -e EVENTS_TOPIC="events" \
    -e FAULT_INJECTOR_URL="http://fault-injector:9000" \
    "${image}" "$@"
}

# log_step prints a banner so the topic guide's commands are easy to
# find in the script output.
log_step() {
  printf '\n=== %s ===\n' "$*"
}

# require_cmd kills the script if the named command isn't on PATH.
require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo >&2 "missing dependency: $1"; exit 1; }
}

require_cmd docker
require_cmd jq
