#!/usr/bin/env bash
# Capture an environment fingerprint students cite in their review.
# Writes perf/results/env.txt (human-readable) and perf/results/meta.json
# (machine-readable) per the topic-254 guide. meta.json records the
# cache software + version, PoP count, shield config, origin limits, all
# tool versions, and inter-node RTTs.

set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

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

# Live edge config (best-effort; empty if the stack isn't up yet).
shield_cfg="$(curl -fsS "${SHIELD_URL}/admin/config" 2>/dev/null || echo '{}')"
pop1_cfg="$(curl -fsS "${POP1_URL}/admin/config" 2>/dev/null || echo '{}')"
origin_cfg="$(curl -fsS "${ORIGIN_URL}/admin/config" 2>/dev/null || echo '{}')"

# Inter-node RTTs measured from the host through each hop (ms, best-effort).
rtt() {
  local url="$1"
  curl -fsS -o /dev/null -w '%{time_total}' "${url}/healthz" 2>/dev/null || echo "n/a"
}
rtt_pop="$(rtt "${POP1_URL}")"
rtt_shield="$(rtt "${SHIELD_URL}")"
rtt_origin="$(rtt "${ORIGIN_URL}")"

{
  echo "# Lab 6-2 environment fingerprint"
  echo "# Captured: $ts"
  echo
  echo "## Host"
  echo "- uname: $host_uname"
  echo "- os: $host_os"
  echo "- arch: $host_arch"
  echo "- cpu_count: $cpu_count"
  echo
  echo "## Tooling"
  echo "- $docker_v"
  echo "- $compose_v"
  echo "- $make_v"
  echo "- $jq_v"
  echo "- $python_v"
  echo
  echo "## Edge"
  echo "- cache software: instrumented Go caching proxy (cmd/cache-proxy), one binary, role=pop|shield"
  echo "- PoP count: 3 (pop-1, pop-2, pop-3)"
  echo "- shield config: ${shield_cfg}"
  echo "- pop-1 config: ${pop1_cfg}"
  echo "- origin config: ${origin_cfg}"
  echo
  echo "## RTTs (host->node /healthz, seconds)"
  echo "- host->pop:    ${rtt_pop}"
  echo "- host->shield: ${rtt_shield}"
  echo "- host->origin: ${rtt_origin}"
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

jq -n \
  --arg captured_at "$ts" \
  --arg uname "$host_uname" \
  --arg os "$host_os" \
  --arg arch "$host_arch" \
  --arg cpu "$cpu_count" \
  --arg docker "$docker_v" \
  --arg compose "$compose_v" \
  --arg jq "$jq_v" \
  --arg python "$python_v" \
  --argjson shield "${shield_cfg:-{}}" \
  --argjson pop1 "${pop1_cfg:-{}}" \
  --argjson origin "${origin_cfg:-{}}" \
  --arg rtt_pop "$rtt_pop" \
  --arg rtt_shield "$rtt_shield" \
  --arg rtt_origin "$rtt_origin" \
  --arg commit "$(git rev-parse HEAD 2>/dev/null || echo unknown)" \
  --arg branch "$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)" \
  '{
     captured_at: $captured_at,
     host: {uname:$uname, os:$os, arch:$arch, cpu_count:$cpu},
     tooling: {docker:$docker, compose:$compose, jq:$jq, python:$python},
     cache: {
       software: "instrumented Go caching proxy (cmd/cache-proxy)",
       version: "lab6-2",
       pop_count: 3,
       shield_config: $shield,
       pop1_config: $pop1
     },
     origin: $origin,
     rtts_seconds: {host_to_pop:$rtt_pop, host_to_shield:$rtt_shield, host_to_origin:$rtt_origin},
     git: {commit:$commit, branch:$branch}
   }' >"$OUT_JSON"

echo "Wrote $OUT_TXT and $OUT_JSON"
