#!/usr/bin/env bash
# apply-fix CANDIDATE=<...> flips a real runtime mode (no rebuild):
#   naive-consumer | idempotent-consumer  -> consumer dedup behaviour
#   naive-dualwrite | outbox              -> producer write/publish path
# The flip is done via the services' admin endpoints so the running
# containers change behaviour immediately.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

CANDIDATE="${CANDIDATE:-}"
[[ -z "${CANDIDATE}" ]] && { echo "usage: make apply-fix CANDIDATE=naive-consumer|idempotent-consumer|naive-dualwrite|outbox"; exit 2; }

case "${CANDIDATE}" in
  naive-consumer|idempotent-consumer)
    log_step "apply-fix: consumer mode -> ${CANDIDATE}"
    set_consumer_mode "${CANDIDATE}"
    for c in "${CONSUMERS[@]}"; do
      printf '  %s -> ' "${c}"; curl -fsS "${c}/state" || true; echo
    done
    ;;
  naive-dualwrite|outbox)
    log_step "apply-fix: producer publish mode -> ${CANDIDATE}"
    producer_config "{\"publish_mode\":\"${CANDIDATE}\"}"
    curl -fsS "${PRODUCER}/state"; echo
    ;;
  *)
    echo "unknown CANDIDATE=${CANDIDATE}"
    exit 2
    ;;
esac

echo
echo "apply-fix: ok (${CANDIDATE})"
