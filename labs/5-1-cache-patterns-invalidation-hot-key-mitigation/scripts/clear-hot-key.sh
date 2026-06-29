#!/usr/bin/env bash
# clear-hot-key - stop the load, forget the active hot key, and disable
# the local LRU so the next experiment starts clean.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
cd "${LAB_ROOT}"

echo "[clear-hot-key] stopping loadgen"
curl -fsS -X POST "${LOADGEN}/stop" >/dev/null || true

rm -f perf/results/active-hotkey.txt || true

echo "[clear-hot-key] disabling local LRU"
app_config '{"local_lru":false}' >/dev/null
echo "[clear-hot-key] active config -> $(curl -fsS "${APP}/admin/config")"
