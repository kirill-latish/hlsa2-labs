#!/usr/bin/env bash
# Capture an environment fingerprint students cite in their review.
# Writes perf/results/env.txt (human-readable) + perf/results/env.json
# (machine-readable) so analyzers can read them without parsing prose.

set -euo pipefail

OUT_TXT="perf/results/env.txt"
OUT_JSON="perf/results/env.json"
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
go_v="$(go version 2>/dev/null || echo 'go (host) NOT FOUND - OK if you build only in containers')"

{
  echo "# Lab 3-3 environment fingerprint"
  echo "# Captured: $ts"
  echo
  echo "## Host"
  echo "- uname: $host_uname"
  echo "- os: $host_os"
  echo "- arch: $host_arch"
  echo "- cpu_count: $cpu_count"
  echo "- mem_total: $mem_total"
  echo
  echo "## Tooling"
  echo "- $docker_v"
  echo "- $compose_v"
  echo "- $make_v"
  echo "- $jq_v"
  echo "- $python_v"
  echo "- $go_v"
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

# Machine-readable form
python3 - <<PY >"$OUT_JSON"
import json, os, datetime, subprocess

def safe(cmd, default="unknown"):
    try:
        return subprocess.check_output(cmd, shell=True, text=True, stderr=subprocess.DEVNULL).strip()
    except subprocess.CalledProcessError:
        return default

d = {
  "captured_at": "$ts",
  "host": {
    "uname": "$host_uname".strip(),
    "os": "$host_os".strip(),
    "arch": "$host_arch".strip(),
    "cpu_count": "$cpu_count".strip(),
    "mem_total": "$mem_total".strip(),
  },
  "tooling": {
    "docker": "$docker_v".strip(),
    "compose": "$compose_v".strip(),
    "make": "$make_v".strip(),
    "jq": "$jq_v".strip(),
    "python": "$python_v".strip(),
  },
  "git": {
    "commit": safe("git rev-parse HEAD 2>/dev/null"),
    "branch": safe("git rev-parse --abbrev-ref HEAD 2>/dev/null"),
  }
}
print(json.dumps(d, indent=2))
PY

echo "Wrote $OUT_TXT and $OUT_JSON"
