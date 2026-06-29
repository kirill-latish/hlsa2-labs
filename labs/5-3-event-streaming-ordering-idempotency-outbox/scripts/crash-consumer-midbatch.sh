#!/usr/bin/env bash
# crash-consumer-midbatch - arm a one-shot mid-batch crash on every
# consumer, then drive a short burst so each consumer dies partway
# through a batch BEFORE committing offsets. The restart policy brings
# them back and the uncommitted tail is redelivered. A naive consumer
# double-applies the redelivered events; an idempotent one suppresses
# them. This is what verify-exactly-once then measures.
#
# Env / make vars:
#   DURATION  burst length (default 20s)
#   RATE_EPS  events/s     (default 200)
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

DUR="$(to_seconds "${DURATION:-20s}")"
RATE_EPS="${RATE_EPS:-200}"

log_step "arming mid-batch crash on all consumers"
for c in "${CONSUMERS[@]}"; do
  curl -fsS -X POST "${c}/admin/crash" >/dev/null || true
done

log_step "driving ${DUR}s burst so the crash lands mid-batch"
producer_run "${RATE_EPS}" "${DUR}" "crash-midbatch"

log_step "waiting for consumers to restart + redeliver"
sleep 5
for c in "${CONSUMERS[@]}"; do
  for _ in $(seq 1 30); do
    curl -fsS "${c}/healthz" >/dev/null 2>&1 && break
    sleep 1
  done
done
wait_consumer_drained 120

echo
echo "crash-consumer-midbatch: ok (consumers restarted and drained the redelivered tail)"
