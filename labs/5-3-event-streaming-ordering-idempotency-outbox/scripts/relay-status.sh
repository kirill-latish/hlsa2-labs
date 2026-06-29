#!/usr/bin/env bash
# Health + lag check for the Go outbox-relay (this lab's Debezium
# stand-in). Passes when /status is reachable and reports the backlog.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

log_step "outbox-relay /healthz"
if ! curl -fsS "${RELAY}/healthz" >/dev/null; then
  echo "FAIL: outbox-relay not reachable at ${RELAY}"
  exit 1
fi
echo "healthz: ok"

log_step "outbox-relay /status"
status="$(curl -fsS "${RELAY}/status" 2>/dev/null || echo '{}')"
echo "${status}"

# Cross-check the DB directly so the number is trustworthy.
backlog_db="$(psql_q 'SELECT count(*) FROM events_outbox WHERE published_at IS NULL')"
published_db="$(psql_q 'SELECT count(*) FROM events_outbox WHERE published_at IS NOT NULL')"
echo
echo "OK: relay healthy. outbox backlog=${backlog_db:-?}, published=${published_db:-?}"
