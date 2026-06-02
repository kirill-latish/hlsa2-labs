#!/usr/bin/env bash
# Capture environment fingerprint to perf/results/env.txt + meta.json.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${LAB_ROOT}/perf/results"
mkdir -p "${OUT_DIR}"
ENV_TXT="${OUT_DIR}/env.txt"
META="${OUT_DIR}/meta.json"

{
  echo "# HLSA2 lab 4-2 environment fingerprint"
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
  echo "## Postgres versions"
  for svc in postgres-primary postgres-replica-1 postgres-replica-2 payment-pg inventory-pg shipping-pg; do
    v=$(docker compose exec -T "${svc}" psql -U hlsa -d hlsa -tAc 'select version()' 2>/dev/null || \
        docker compose exec -T "${svc}" psql -U "${svc%-pg}" -d "${svc%-pg}" -tAc 'select version()' 2>/dev/null || \
        echo "unavailable")
    printf '%s: %s\n' "${svc}" "${v}"
  done
  echo
  echo "## Redpanda version"
  docker compose exec -T redpanda rpk version 2>/dev/null || true
  echo
  echo "## Inter-container ping (round-trip ms)"
  for src in postgres-primary orchestrator outbox-relay; do
    for dst in postgres-primary postgres-replica-1 postgres-replica-2 redpanda payment-pg; do
      [ "${src}" = "${dst}" ] && continue
      out=$(docker compose exec -T "${src}" sh -c "ping -c 3 -W 1 ${dst} 2>/dev/null | tail -1" 2>/dev/null || true)
      printf '%s -> %s: %s\n' "${src}" "${dst}" "${out:-unavailable}"
    done
  done
} | tee "${ENV_TXT}"

# meta.json is a structured digest used by the analyzers + check-submission.
{
  jq -n \
    --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
    --arg uname "$(uname -a)" \
    --arg docker "$(docker --version)" \
    --arg compose "$(docker compose version | head -1)" \
    --arg replicas "2" \
    '{captured_at: $ts, uname: $uname, docker: $docker, compose: $compose, replica_count: ($replicas|tonumber)}'
} > "${META}"

echo
echo "Wrote ${ENV_TXT} + ${META}"
