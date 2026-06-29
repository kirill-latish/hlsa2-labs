#!/usr/bin/env bash
# Capture an environment fingerprint students cite in their review.
# Writes perf/results/env.txt (human-readable) + perf/results/meta.json
# (machine-readable) recording node counts, per-node memory limits, all
# version strings, and approximate inter-node round-trip times.

set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
if [[ -f .env ]]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi
APP="http://localhost:${LAB_APP_PORT:-8080}"

OUT_TXT="perf/results/env.txt"
OUT_JSON="perf/results/meta.json"
mkdir -p "$(dirname "$OUT_TXT")"

ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
host_uname="$(uname -a 2>/dev/null || echo unknown)"
host_arch="$(uname -m 2>/dev/null || echo unknown)"
host_os="$(uname -s 2>/dev/null || echo unknown)"
cpu_count="$(getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo unknown)"

docker_v="$(docker --version 2>/dev/null || echo 'docker NOT FOUND')"
compose_v="$(docker compose version 2>/dev/null | head -1 || echo 'docker compose NOT FOUND')"
make_v="$(make --version 2>/dev/null | head -1 || echo 'make NOT FOUND')"
jq_v="$(jq --version 2>/dev/null || echo 'jq NOT FOUND')"
python_v="$(python3 --version 2>/dev/null || echo 'python3 NOT FOUND')"

# Container version strings.
redis_v="$(docker compose exec -T redis-1 redis-server --version 2>/dev/null | tr -d '\r' || echo unknown)"
pg_v="$(docker compose exec -T postgres psql -U hlsa -d hlsa -tAc 'SHOW server_version' 2>/dev/null | tr -d '\r' || echo unknown)"

# Per-node maxmemory (the memory limit baked into compose).
redis_maxmem="$(docker compose exec -T redis-1 redis-cli config get maxmemory 2>/dev/null | tail -1 | tr -d '\r' || echo unknown)"

# Approximate RTTs (host-side timings; sub-ms on the compose bridge).
rtt_app_ms="$(curl -o /dev/null -s -w '%{time_total}' "${APP}/healthz" 2>/dev/null || echo 0)"
rtt_redis_ms="$( { /usr/bin/time -p docker compose exec -T redis-1 redis-cli ping ; } 2>&1 | awk '/real/ {print $2}' || echo 0)"

{
  echo "# Lab 5-1 environment fingerprint"
  echo "# Captured: $ts"
  echo
  echo "## Topology (node counts)"
  echo "- redis shards: 3 (redis-1, redis-2, redis-3, client-side sharded)"
  echo "- app: 1"
  echo "- postgres (system of record): 1"
  echo "- loadgen: 1"
  echo
  echo "## Host"
  echo "- uname: $host_uname"
  echo "- os: $host_os"
  echo "- arch: $host_arch"
  echo "- cpu_count: $cpu_count"
  echo
  echo "## Versions"
  echo "- $docker_v"
  echo "- $compose_v"
  echo "- $make_v"
  echo "- $jq_v"
  echo "- $python_v"
  echo "- redis: $redis_v"
  echo "- postgres: $pg_v"
  echo "- redis maxmemory (per node): $redis_maxmem"
  echo
  echo "## Approx RTT"
  echo "- host->app /healthz: ${rtt_app_ms}s"
  echo "- redis-1 PING (incl docker exec): ${rtt_redis_ms}s"
  echo
  echo "## Lab git"
  if git rev-parse HEAD >/dev/null 2>&1; then
    echo "- commit: $(git rev-parse HEAD)"
    echo "- branch: $(git rev-parse --abbrev-ref HEAD)"
  else
    echo "- not a git checkout (or git not installed)"
  fi
} | tee "$OUT_TXT" >/dev/null

python3 - "$ts" "$host_uname" "$host_os" "$host_arch" "$cpu_count" \
  "$docker_v" "$compose_v" "$redis_v" "$pg_v" "$redis_maxmem" \
  "$rtt_app_ms" "$rtt_redis_ms" >"$OUT_JSON" <<'PY'
import json, sys
(ts, uname, os_, arch, cpu, docker_v, compose_v, redis_v, pg_v, maxmem,
 rtt_app, rtt_redis) = sys.argv[1:13]
d = {
  "captured_at": ts,
  "topology": {"redis_shards": 3, "app": 1, "postgres": 1, "loadgen": 1},
  "host": {"uname": uname, "os": os_, "arch": arch, "cpu_count": cpu},
  "versions": {
    "docker": docker_v, "compose": compose_v,
    "redis": redis_v, "postgres": pg_v,
    "redis_maxmemory_per_node": maxmem,
  },
  "rtt": {"host_to_app_s": rtt_app, "redis_ping_s": rtt_redis},
}
print(json.dumps(d, indent=2))
PY

echo "Wrote $OUT_TXT and $OUT_JSON"
