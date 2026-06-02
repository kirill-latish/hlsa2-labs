#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

SERVICE="${SERVICE:?SERVICE is required}"
log_step "clear-fault service=${SERVICE}"
docker compose exec -T fault-injector wget -qO- --method=DELETE "http://localhost:9000/faults/${SERVICE}" || true
echo
