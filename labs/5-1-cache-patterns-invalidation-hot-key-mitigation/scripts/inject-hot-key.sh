#!/usr/bin/env bash
# inject-hot-key - record the hot key + weight that subsequent
# `make bench-hot-key` runs will use. Because the app shards by key
# hash, this key deterministically lands on ONE Redis node, so a single
# popular key overloads one shard while the others idle.
#
# Usage: make inject-hot-key KEY=celebrity-1 WEIGHT=0.4
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
cd "${LAB_ROOT}"

KEY="${KEY:?KEY=<hot key name> is required}"
WEIGHT="${WEIGHT:?WEIGHT=<0..1 fraction of traffic> is required}"

mkdir -p perf/results
{
  echo "key=${KEY}"
  echo "weight=${WEIGHT}"
  echo "applied_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} >perf/results/active-hotkey.txt

echo "[inject-hot-key] key=${KEY} weight=${WEIGHT}"
echo "[inject-hot-key] recorded in perf/results/active-hotkey.txt"
echo "Next: make bench-hot-key DURATION=3m LABEL=hot-baseline"
