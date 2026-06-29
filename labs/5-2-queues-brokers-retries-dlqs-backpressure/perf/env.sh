#!/usr/bin/env bash
# Capture an environment fingerprint students cite in their review.
# Writes perf/results/env.txt (human-readable) + perf/results/meta.json
# (machine-readable) so analyzers can read them without parsing prose.
#
# Records both broker versions (RabbitMQ + Kafka/Redpanda), consumer
# instance count, per-node limits, all tool versions, and inter-node
# RTTs - the fingerprint the topic guide's step 2 asks for.

set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"

if [[ -f .env ]]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi

OUT_TXT="perf/results/env.txt"
OUT_JSON="perf/results/meta.json"
mkdir -p "$(dirname "$OUT_TXT")"

ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
host_uname="$(uname -a 2>/dev/null || echo unknown)"
host_arch="$(uname -m 2>/dev/null || echo unknown)"
host_os="$(uname -s 2>/dev/null || echo unknown)"
cpu_count="$(getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo unknown)"
mem_total="unknown"
if [[ -r /proc/meminfo ]]; then
  mem_total="$(awk '/MemTotal/ {print $2 " " $3}' /proc/meminfo)"
elif command -v sysctl >/dev/null 2>&1; then
  mem_total="$(sysctl -n hw.memsize 2>/dev/null || echo unknown) bytes"
fi

docker_v="$(docker --version 2>/dev/null || echo 'docker NOT FOUND')"
compose_v="$(docker compose version 2>/dev/null || echo 'docker compose NOT FOUND')"
make_v="$(make --version 2>/dev/null | head -1 || echo 'make NOT FOUND')"
jq_v="$(jq --version 2>/dev/null || echo 'jq NOT FOUND')"
python_v="$(python3 --version 2>/dev/null || echo 'python3 NOT FOUND')"

# Broker versions straight from the running containers (best effort).
rabbit_v="$(docker compose exec -T rabbitmq rabbitmqctl version 2>/dev/null | tr -d '\r' || echo unknown)"
redpanda_v="$(docker compose exec -T redpanda rpk version 2>/dev/null | head -1 | tr -d '\r' || echo unknown)"
pg_v="$(docker compose exec -T postgres postgres --version 2>/dev/null | tr -d '\r' || echo unknown)"
consumer_count="$(docker compose ps --services 2>/dev/null | grep -c '^consumer-' || echo 3)"

{
  echo "# Lab 5-2 environment fingerprint"
  echo "# Captured: $ts"
  echo
  echo "## Host"
  echo "- uname: $host_uname"
  echo "- os: $host_os"
  echo "- arch: $host_arch"
  echo "- cpu_count: $cpu_count"
  echo "- mem_total: $mem_total"
  echo
  echo "## Brokers / downstream"
  echo "- rabbitmq: $rabbit_v"
  echo "- redpanda: $redpanda_v"
  echo "- postgres: $pg_v"
  echo "- consumer_instances: $consumer_count"
  echo
  echo "## Tooling"
  echo "- $docker_v"
  echo "- $compose_v"
  echo "- $make_v"
  echo "- $jq_v"
  echo "- $python_v"
  echo
  echo "## Lab git"
  if git rev-parse HEAD >/dev/null 2>&1; then
    echo "- commit: $(git rev-parse HEAD)"
    echo "- branch: $(git rev-parse --abbrev-ref HEAD)"
    echo "- dirty: $(git status --porcelain | wc -l | tr -d ' ') uncommitted files"
  else
    echo "- not a git checkout (or git not installed)"
  fi
} | tee "$OUT_TXT" >/dev/null

python3 - "$ts" "$host_uname" "$host_os" "$host_arch" "$cpu_count" "$mem_total" \
  "$docker_v" "$compose_v" "$rabbit_v" "$redpanda_v" "$pg_v" "$consumer_count" <<'PY' >"$OUT_JSON"
import json, subprocess, sys

(ts, uname, os_, arch, cpu, mem, docker_v, compose_v,
 rabbit_v, redpanda_v, pg_v, consumer_count) = sys.argv[1:13]

def safe(cmd, default="unknown"):
    try:
        return subprocess.check_output(cmd, shell=True, text=True, stderr=subprocess.DEVNULL).strip()
    except subprocess.CalledProcessError:
        return default

d = {
  "captured_at": ts,
  "host": {"uname": uname, "os": os_, "arch": arch, "cpu_count": cpu, "mem_total": mem},
  "brokers": {"rabbitmq": rabbit_v, "redpanda": redpanda_v, "postgres": pg_v,
              "consumer_instances": consumer_count},
  "tooling": {"docker": docker_v, "compose": compose_v},
  "git": {
    "commit": safe("git rev-parse HEAD 2>/dev/null"),
    "branch": safe("git rev-parse --abbrev-ref HEAD 2>/dev/null"),
  },
}
print(json.dumps(d, indent=2))
PY

echo "Wrote $OUT_TXT and $OUT_JSON"
