#!/usr/bin/env bash
# clear-stampede - stop the load generator, flush the cache, and reset
# the app config back to the unmitigated baseline (no coalescing, no
# jitter, no local LRU) so the next experiment starts from a known state.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
cd "${LAB_ROOT}"

echo "[clear-stampede] stopping loadgen"
curl -fsS -X POST "${LOADGEN}/stop" >/dev/null || true

echo "[clear-stampede] flushing cache"
curl -fsS -X POST "${APP}/admin/flush" >/dev/null || true

echo "[clear-stampede] resetting config to baseline"
app_config '{"coalescing":"none","ttl_jitter_pct":0,"local_lru":false}' >/dev/null
echo "[clear-stampede] active config -> $(curl -fsS "${APP}/admin/config")"
