#!/usr/bin/env bash
# Reset consumer group offsets to 0 and wait for the consumer to drain.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

CONSUMER_MODE="${CONSUMER_MODE:-idempotent}"
WINDOW="${WINDOW:-24h}"

log_step "replay CONSUMER_MODE=${CONSUMER_MODE} WINDOW=${WINDOW}"

# Restart the consumer with the requested mode so the group reads
# from the start. The group is fixed; we delete-and-recreate.
docker compose exec -T redpanda rpk group delete lab42-consumer 2>/dev/null || true

# Restart consumer with the chosen mode.
docker compose stop consumer
CONSUMER_MODE="${CONSUMER_MODE}" docker compose up -d consumer
docker compose exec -T consumer wget -qO- "http://localhost:9103/healthz" || true

# Drive a fresh window of synthetic events. Same seed => same stream.
WINDOW="${WINDOW}" SEED="1" bash "${SCRIPTS_ROOT}/seed-events.sh"

# Give the consumer time to drain the seeded events.
log_step "waiting ${REPLAY_WAIT_S:-15}s for consumer to drain"
sleep "${REPLAY_WAIT_S:-15}"

# Snapshot the resulting state.
HASH=$(docker compose exec -T payment-pg psql -U payment -d payment -tAc "
  SELECT md5(string_agg(user_id || ':' || balance, ',' ORDER BY user_id))
  FROM accounts
")
mkdir -p "${RESULTS_ROOT}/replay/${CONSUMER_MODE}"
echo "${HASH}" > "${RESULTS_ROOT}/replay/${CONSUMER_MODE}/state-hash.txt"

echo "state hash (${CONSUMER_MODE}): ${HASH}"
