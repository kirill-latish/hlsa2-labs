#!/usr/bin/env bash
# Capture the Lab 6-1 environment fingerprint students cite in their
# review. Writes perf/results/env.txt (human-readable) and
# perf/results/meta.json (machine-readable: proxy type+version, backend
# count, versions, and best-effort inter-node RTTs).

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

EDGE="http://localhost:${LAB_EDGE_PORT:-8080}"
LOADGEN="http://localhost:${LAB_LOADGEN_PORT:-8090}"

# The edge is an instrumented Go reverse proxy (see README).
proxy_type="instrumented-go-reverse-proxy"
proxy_version="$(curl -fsS "${EDGE}/admin/config" 2>/dev/null || echo '{}')"
backend_count=4

# Best-effort RTTs measured with curl's time_total against the local
# control surfaces. edge->backend and edge->postgres are reported from
# inside the cluster via the edge's own health-check latency where
# available; here we capture the loadgen->edge hop the host can see.
rtt_loadgen_edge="$(curl -fsS -o /dev/null -w '%{time_total}' "${EDGE}/healthz" 2>/dev/null || echo 'n/a')"
rtt_host_loadgen="$(curl -fsS -o /dev/null -w '%{time_total}' "${LOADGEN}/healthz" 2>/dev/null || echo 'n/a')"

{
  echo "# Lab 6-1 environment fingerprint"
  echo "# Captured: $ts"
  echo
  echo "## Edge tier"
  echo "- proxy_type: $proxy_type (Go reverse proxy; NGINX/Envoy/HAProxy are the documented production option)"
  echo "- backend_count: $backend_count"
  echo "- edge config: $proxy_version"
  echo
  echo "## Host"
  echo "- uname: $host_uname"
  echo "- os: $host_os"
  echo "- arch: $host_arch"
  echo "- cpu_count: $cpu_count"
  echo "- mem_total: $mem_total"
  echo
  echo "## Tooling / image versions"
  echo "- $docker_v"
  echo "- $compose_v"
  echo "- $make_v"
  echo "- $jq_v"
  echo "- $python_v"
  echo "- prometheus image: prom/prometheus:v2.55.1"
  echo "- grafana image: grafana/grafana:11.2.2"
  echo "- postgres image: postgres:16-alpine"
  echo "- go toolchain: golang:1.22-alpine (build only)"
  echo
  echo "## Inter-node RTT (best-effort, time_total seconds)"
  echo "- host -> edge /healthz: $rtt_loadgen_edge"
  echo "- host -> loadgen /healthz: $rtt_host_loadgen"
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

python3 - "$ts" "$proxy_type" "$backend_count" "$host_uname" "$host_os" "$host_arch" \
  "$cpu_count" "$mem_total" "$docker_v" "$compose_v" "$rtt_loadgen_edge" "$rtt_host_loadgen" >"$OUT_JSON" <<'PY'
import json, subprocess, sys

(_, ts, proxy_type, backend_count, uname, os_, arch, cpu, mem,
 docker_v, compose_v, rtt_le, rtt_hl) = sys.argv

def safe(cmd, default="unknown"):
    try:
        return subprocess.check_output(cmd, shell=True, text=True,
                                       stderr=subprocess.DEVNULL).strip()
    except subprocess.CalledProcessError:
        return default

d = {
  "captured_at": ts,
  "edge": {
    "proxy_type": proxy_type,
    "proxy_family_option": "nginx|envoy|haproxy (documented production alternative)",
    "backend_count": int(backend_count),
  },
  "images": {
    "prometheus": "prom/prometheus:v2.55.1",
    "grafana": "grafana/grafana:11.2.2",
    "postgres": "postgres:16-alpine",
    "go_toolchain": "golang:1.22-alpine",
  },
  "host": {
    "uname": uname.strip(), "os": os_.strip(), "arch": arch.strip(),
    "cpu_count": cpu.strip(), "mem_total": mem.strip(),
  },
  "tooling": {"docker": docker_v.strip(), "compose": compose_v.strip()},
  "rtt_seconds": {"host_to_edge": rtt_le, "host_to_loadgen": rtt_hl},
  "git": {
    "commit": safe("git rev-parse HEAD 2>/dev/null"),
    "branch": safe("git rev-parse --abbrev-ref HEAD 2>/dev/null"),
  },
}
print(json.dumps(d, indent=2))
PY

echo "Wrote $OUT_TXT and $OUT_JSON"
