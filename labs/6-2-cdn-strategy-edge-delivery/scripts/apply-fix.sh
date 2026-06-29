#!/usr/bin/env bash
# apply-fix - POST a config change to the PoPs (and where relevant the
# shield/origin) to enact one of the named remediation candidates. Each
# candidate maps directly to a step in the topic-254 guide.
#
#   strip-tracking-params   PoP cache key -> stripped-allowlist (drop tracking)
#   broad-key-personalized  PoP personalized_mode -> broad-key-ignores-auth (the BUG)
#   private-personalized    PoP personalized_mode -> private-no-store (the fix)
#   shield-off              PoP shield_routing -> false (misses hit origin directly)
#   shield-on               PoP shield_routing -> true  (misses funnel through shield)
#   stale-if-error          PoP + shield stale_if_error -> true
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

CANDIDATE="${CANDIDATE:?CANDIDATE=strip-tracking-params|broad-key-personalized|private-personalized|shield-off|shield-on|stale-if-error}"

case "${CANDIDATE}" in
  strip-tracking-params)
    push_config_pops '{"cache_key_mode":"stripped-allowlist","allowlist":["v"]}'
    ;;
  broad-key-personalized)
    # The misconfiguration: cache personalized responses under a key that
    # ignores the auth cookie. Flush first so the leak is reproducible.
    flush_edge
    push_config_pops '{"personalized_mode":"broad-key-ignores-auth"}'
    ;;
  private-personalized)
    flush_edge
    push_config_pops '{"personalized_mode":"private-no-store"}'
    ;;
  shield-off)
    push_config_pops '{"shield_routing":false}'
    ;;
  shield-on)
    push_config_pops '{"shield_routing":true}'
    ;;
  stale-if-error)
    push_config_pops '{"stale_if_error":true}'
    push_config_shield '{"stale_if_error":true}'
    ;;
  *)
    echo "ERROR: unknown CANDIDATE=${CANDIDATE}" >&2
    exit 2
    ;;
esac

echo "[apply-fix] CANDIDATE=${CANDIDATE} applied."
echo "[apply-fix] pop-1 config now -> $(curl -fsS "${POP1_URL}/admin/config" | jq -c '.config')"
