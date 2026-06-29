#!/usr/bin/env bash
# inject-crash-between-writes LABEL=<...> - drive the producer in its
# current publish mode (set by apply-fix) and crash it AFTER the DB
# commit but BEFORE the publish.
#
#   naive-dualwrite: DB row commits, event never published -> orphan.
#   outbox:          business row + outbox row commit atomically; the
#                    relay still ships the outbox row -> no orphan.
#
# Resets the consistency tables first so analyze-consistency measures
# just this phase. The companion analyze-consistency LABEL=<...> reads
# the resulting orphan count.
#
# Env / make vars:
#   LABEL        perf/results subdir          (default dualwrite)
#   CRASH_AFTER  successful writes before crash (default 10)
#   RATE_EPS     events/s                       (default 100)
#   DURATION     drive window                   (default 15s)
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

LABEL="${LABEL:-dualwrite}"
CRASH_AFTER="${CRASH_AFTER:-10}"
RATE_EPS="${RATE_EPS:-100}"
DUR="$(to_seconds "${DURATION:-15s}")"

mode="$(curl -fsS "${PRODUCER}/state" 2>/dev/null | python3 -c 'import sys,json;print(json.load(sys.stdin).get("publish_mode","?"))' 2>/dev/null || echo '?')"
log_step "inject-crash-between-writes LABEL=${LABEL} (producer publish_mode=${mode})"
if [[ "${mode}" != "naive-dualwrite" && "${mode}" != "outbox" ]]; then
  echo "WARN: producer publish_mode is '${mode}'. Run 'make apply-fix CANDIDATE=naive-dualwrite|outbox' first."
fi

log_step "reset orders / events_outbox / projection / side_effects / processed_ids"
psql_exec "TRUNCATE orders, events_outbox, projection, side_effects, processed_ids RESTART IDENTITY;"
set_consumer_mode "idempotent-consumer"
set_replay_mode "off"

log_step "arm crash after ${CRASH_AFTER} committed writes, then drive"
curl -fsS -X POST "${PRODUCER}/admin/arm-crash" -H 'content-type: application/json' \
  -d "{\"after\":${CRASH_AFTER}}" >/dev/null || true
producer_run "${RATE_EPS}" "${DUR}" "${LABEL}"

log_step "waiting for producer to restart"
sleep 5
for _ in $(seq 1 40); do
  curl -fsS "${PRODUCER}/healthz" >/dev/null 2>&1 && break
  sleep 1
done

# Give the relay (outbox mode) + consumers time to ship/apply committed rows.
wait_outbox_drained 60
wait_consumer_drained 60
sleep 3

echo
echo "inject-crash-between-writes: ok. Next: make analyze-consistency LABEL=${LABEL}"
