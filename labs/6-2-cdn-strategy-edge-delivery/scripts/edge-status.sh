#!/usr/bin/env bash
# edge-status - show each PoP + the shield: role, live cache config,
# cache-entry count, and health. This is the "is the edge wired up?"
# sanity check the topic guide's step 1 expects to pass.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

show() {
  local name="$1" url="$2"
  local cfg health
  health="$(curl -fsS "${url}/healthz" 2>/dev/null || echo 'DOWN')"
  cfg="$(curl -fsS "${url}/admin/config" 2>/dev/null || echo '{}')"
  echo "=== ${name} (${url}) health=${health} ==="
  echo "${cfg}" | jq '{node, role, cache_entries, config}'
  echo
}

echo "Edge status"
echo "==========="
show "shield" "${SHIELD_URL}"
show "pop-1"  "${POP1_URL}"
show "pop-2"  "${POP2_URL}"
show "pop-3"  "${POP3_URL}"
echo "=== origin (${ORIGIN_URL}) ==="
curl -fsS "${ORIGIN_URL}/admin/config" 2>/dev/null | jq '.' || echo 'DOWN'
