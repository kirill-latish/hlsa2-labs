#!/usr/bin/env bash
# apply-fix - POST a cache-pattern change to the app's /admin/config.
# Mitigations stack: jitter is the always-on default you turn on first,
# then ONE coalescing mode, then (for the hot key) local-LRU.
#
# Usage:
#   make apply-fix CANDIDATE=jitter JITTER=20pct
#   make apply-fix CANDIDATE=singleflight
#   make apply-fix CANDIDATE=xfetch
#   make apply-fix CANDIDATE=swr
#   make apply-fix CANDIDATE=local-lru LOCAL_SIZE=1000 LOCAL_TTL=5s
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
cd "${LAB_ROOT}"

CANDIDATE="${CANDIDATE:?CANDIDATE=jitter|singleflight|xfetch|swr|local-lru is required}"

case "${CANDIDATE}" in
  jitter)
    pct="$(pct_to_num "${JITTER:-20pct}")"
    body="$(jq -n --argjson j "${pct}" '{ttl_jitter_pct:$j}')"
    ;;
  singleflight|xfetch|swr)
    body="$(jq -n --arg c "${CANDIDATE}" '{coalescing:$c}')"
    ;;
  local-lru)
    size="${LOCAL_SIZE:-1000}"
    ttl_s="$(dur_to_seconds "${LOCAL_TTL:-5s}")"
    body="$(jq -n --argjson s "${size}" --argjson t "${ttl_s}" \
              '{local_lru:true, local_lru_size:$s, local_lru_ttl_seconds:$t}')"
    ;;
  *)
    echo "ERROR: CANDIDATE must be jitter|singleflight|xfetch|swr|local-lru" >&2
    exit 2
    ;;
esac

echo "[apply-fix] ${CANDIDATE} -> ${body}"
app_config "${body}"
echo
echo "[apply-fix] active config -> $(curl -fsS "${APP}/admin/config")"
