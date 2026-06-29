#!/usr/bin/env bash
# consumer-status - show the 3 consumer health + state endpoints.
set -uo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"
load_env

declare -a PORTS=(
  "consumer-1:${LAB_CONSUMER1_PORT:-8101}"
  "consumer-2:${LAB_CONSUMER2_PORT:-8102}"
  "consumer-3:${LAB_CONSUMER3_PORT:-8103}"
)

rc=0
healthy=0
for entry in "${PORTS[@]}"; do
  name="${entry%%:*}"
  port="${entry##*:}"
  if H="$(curl -fsS "http://localhost:${port}/healthz" 2>/dev/null)" && [[ "${H}" == "ok" ]]; then
    S="$(curl -fsS "http://localhost:${port}/state" 2>/dev/null || echo '{}')"
    echo "  ${name}: HEALTHY mode=$(echo "${S}" | jq -r '.mode // "?"') max_retries=$(echo "${S}" | jq -r '.max_retries // "?"') processed=$(echo "${S}" | jq -r '.processed // 0')"
    healthy=$((healthy + 1))
  else
    echo "  ${name}: UNREACHABLE (port ${port})"
    rc=1
  fi
done

echo
echo "${healthy}/3 consumers healthy."
exit "${rc}"
