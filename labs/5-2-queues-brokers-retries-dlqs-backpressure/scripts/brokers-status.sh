#!/usr/bin/env bash
# brokers-status - check that both broker families are healthy.
#   RabbitMQ : management API /api/overview + queue depths.
#   Redpanda : rpk cluster info / health.
set -uo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"
load_env

MGMT="http://localhost:${LAB_RABBITMQ_MGMT_PORT:-15672}"
AUTH="guest:guest"
rc=0

echo "== RabbitMQ =="
if OV="$(curl -fsS -u "${AUTH}" "${MGMT}/api/overview" 2>/dev/null)"; then
  echo "  version: $(echo "${OV}" | jq -r '.rabbitmq_version // "?"')"
  echo "  status : RUNNING"
  for q in lab52.work lab52.dlq; do
    Q="$(curl -fsS -u "${AUTH}" "${MGMT}/api/queues/%2F/${q}" 2>/dev/null || echo '{}')"
    echo "  queue ${q}: ready=$(echo "${Q}" | jq -r '.messages_ready // 0') unacked=$(echo "${Q}" | jq -r '.messages_unacknowledged // 0') consumers=$(echo "${Q}" | jq -r '.consumers // 0')"
  done
else
  echo "  status : UNREACHABLE (is rabbitmq up? mgmt port ${LAB_RABBITMQ_MGMT_PORT:-15672})"
  rc=1
fi

echo
echo "== Redpanda =="
if docker compose exec -T redpanda rpk cluster info 2>/dev/null; then
  docker compose exec -T redpanda rpk cluster health 2>/dev/null || true
else
  echo "  status : UNREACHABLE (is redpanda up?)"
  rc=1
fi

echo
if [[ "${rc}" -eq 0 ]]; then
  echo "OK: both brokers healthy."
else
  echo "FAIL: one or more brokers unhealthy."
fi
exit "${rc}"
