#!/usr/bin/env bash
# Seed step: create the Redpanda topic (with the configured partition
# count) and confirm the Postgres schema is present. Idempotent.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

log_step "creating topic ${TOPIC} with ${PARTITIONS} partitions"
rpk topic create "${TOPIC}" --partitions "${PARTITIONS}" --replicas 1 || true

log_step "topic list"
rpk topic list || true

log_step "verifying Postgres schema"
for t in orders events_outbox processed_ids projection side_effects; do
  n="$(psql_q "SELECT to_regclass('public.${t}') IS NOT NULL")"
  printf '  table %-14s %s\n' "${t}" "${n:-MISSING}"
done

echo
echo "seed: ok"
