#!/usr/bin/env bash
# apply-fix - flip the consumer fleet's retry semantics at runtime (and,
# for backpressure-signal, enable producer-side backpressure handling).
# Implemented "for real" by POSTing /admin/config to each consumer (and
# the producer), so the fix engages without recreating containers.
#
#   CANDIDATE = bounded-retry | classify-failures | backpressure-signal
#   MAX_RETRIES = retry cap for the bounded modes (default 5)
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"
load_env

CANDIDATE="${CANDIDATE:?CANDIDATE=bounded-retry|classify-failures|backpressure-signal is required}"
MAX_RETRIES="${MAX_RETRIES:-5}"

case "${CANDIDATE}" in
  bounded-retry|classify-failures|backpressure-signal) ;;
  *)
    echo "ERROR: CANDIDATE must be bounded-retry|classify-failures|backpressure-signal" >&2
    exit 2
    ;;
esac

declare -a PORTS=(
  "${LAB_CONSUMER1_PORT:-8101}"
  "${LAB_CONSUMER2_PORT:-8102}"
  "${LAB_CONSUMER3_PORT:-8103}"
)

echo "[apply-fix] flipping consumer fleet to mode=${CANDIDATE} max_retries=${MAX_RETRIES}"
for port in "${PORTS[@]}"; do
  curl -fsS -X POST -H 'content-type: application/json' \
    -d "$(jq -n --arg m "${CANDIDATE}" --argjson r "${MAX_RETRIES}" '{mode:$m,max_retries:$r}')" \
    "http://localhost:${port}/admin/config" && echo "  consumer @${port} -> ${CANDIDATE}"
done

if [[ "${CANDIDATE}" == "backpressure-signal" ]]; then
  echo "[apply-fix] enabling producer backpressure handling"
  curl -fsS -X POST -H 'content-type: application/json' \
    -d '{"backpressure":true}' \
    "http://localhost:${LAB_PRODUCER_PORT:-8080}/admin/config" >/dev/null \
    && echo "  producer -> backpressure honored"
fi

echo "[apply-fix] done. Verify with: make consumer-status"
