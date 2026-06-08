#!/usr/bin/env bash
# Persist the chosen shard-key strategy as `.candidate`. Subsequent
# `make bench-skew SHARD_KEY=fixed` invocations resolve `fixed` to this
# value (both inside the bench binary and in the loadgen container).
set -euo pipefail
LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CANDIDATE="${CANDIDATE:?CANDIDATE env required (one of hash-suffix, composite-key, resharded)}"

case "${CANDIDATE}" in
  hash-suffix|composite-key|resharded) ;;
  *) echo >&2 "CANDIDATE must be hash-suffix | composite-key | resharded (got: ${CANDIDATE})"; exit 1 ;;
esac

echo -n "${CANDIDATE}" > "${LAB_ROOT}/.candidate"
echo "[lab43] applied fix: ${CANDIDATE}"
echo "[lab43] subsequent bench-skew SHARD_KEY=fixed runs will use this strategy"
