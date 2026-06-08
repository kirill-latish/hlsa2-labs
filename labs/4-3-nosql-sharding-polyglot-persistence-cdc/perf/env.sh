#!/usr/bin/env bash
# Capture environment fingerprint to perf/results/env.txt + meta.json.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${LAB_ROOT}/perf/results"
mkdir -p "${OUT_DIR}"
ENV_TXT="${OUT_DIR}/env.txt"
META="${OUT_DIR}/meta.json"

{
  echo "# HLSA2 lab 4-3 environment fingerprint"
  echo "# Captured: $(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  echo
  echo "## Host"
  uname -a || true
  echo
  echo "## CPU"
  if [ "$(uname)" = "Darwin" ]; then
    sysctl -n machdep.cpu.brand_string || true
    echo "logical_cpus=$(sysctl -n hw.logicalcpu 2>/dev/null || echo unknown)"
  else
    grep -m1 "model name" /proc/cpuinfo 2>/dev/null || true
    echo "logical_cpus=$(grep -c ^processor /proc/cpuinfo 2>/dev/null || echo unknown)"
  fi
  echo
  echo "## Memory"
  if [ "$(uname)" = "Darwin" ]; then
    echo "memsize=$(sysctl -n hw.memsize 2>/dev/null || echo unknown)"
  else
    grep -E "MemTotal|MemAvailable" /proc/meminfo 2>/dev/null || true
  fi
  echo
  echo "## Docker"
  docker --version
  docker compose version
  echo
  echo "## Containers"
  docker compose ps --format json 2>/dev/null | jq -r '.[] | "\(.Name)\t\(.State)\t\(.Health // "-")"' || true
  echo
  echo "## Postgres version"
  docker compose exec -T postgres psql -U hlsa -d hlsa -tAc 'select version()' 2>/dev/null || echo "unavailable"
  echo
  echo "## Mongo version (mongos-1)"
  docker compose exec -T mongos-1 mongosh --quiet --eval 'db.version()' 2>/dev/null || echo "unavailable"
  echo
  echo "## sh.status() (mongos-1, abridged)"
  docker compose exec -T mongos-1 mongosh --quiet --eval 'sh.status({verbose:false})' 2>/dev/null | head -120 || true
  echo
  echo "## Redpanda version"
  docker compose exec -T redpanda rpk version 2>/dev/null || true
  echo
  echo "## Debezium connector"
  curl -fsS http://localhost:"${LAB_DEBEZIUM_PORT:-18083}"/connectors/lab43-postgres/status 2>/dev/null || true
  echo
  echo "## Elasticsearch version"
  curl -fsS http://localhost:"${LAB_ELASTICSEARCH_PORT:-19200}"/ 2>/dev/null | jq -r '.version.number' || true
} | tee "${ENV_TXT}"

# meta.json is a structured digest used by the analyzers + check-submission.
{
  jq -n \
    --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
    --arg uname "$(uname -a)" \
    --arg docker "$(docker --version)" \
    --arg compose "$(docker compose version | head -1)" \
    --arg shards "3" \
    --arg mongos "2" \
    '{captured_at: $ts, uname: $uname, docker: $docker, compose: $compose, shard_count: ($shards|tonumber), mongos_count: ($mongos|tonumber)}'
} > "${META}"

echo
echo "Wrote ${ENV_TXT} + ${META}"
