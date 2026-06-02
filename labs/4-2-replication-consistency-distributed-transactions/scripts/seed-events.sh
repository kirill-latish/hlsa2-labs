#!/usr/bin/env bash
# Produce a synthetic 24h event window directly to Redpanda.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

WINDOW="${WINDOW:-24h}"
RATE="${EVENT_RATE:-50}"
ORDERS="${ORDERS:-200}"
SEED="${SEED:-1}"

# Convert WINDOW to a duration the Go binary understands.
log_step "seed-events window=${WINDOW} rate=${RATE}/s orders=${ORDERS} seed=${SEED}"

docker_run_oneshot seed-events \
  --window="${WINDOW}" \
  --rate="${RATE}" \
  --orders="${ORDERS}" \
  --seed="${SEED}" \
  --brokers="redpanda:9092" \
  --topic="events"
