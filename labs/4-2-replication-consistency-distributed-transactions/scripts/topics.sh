#!/usr/bin/env bash
# Create the Redpanda topics referenced by the topic guide. Idempotent.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

log_step "creating topics events + dlq on Redpanda"
docker compose exec -T redpanda rpk topic create events --partitions 6 --replicas 1 || true
docker compose exec -T redpanda rpk topic create dlq    --partitions 1 --replicas 1 || true

log_step "listing topics"
docker compose exec -T redpanda rpk topic list
