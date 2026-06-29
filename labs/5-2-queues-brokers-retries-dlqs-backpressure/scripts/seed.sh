#!/usr/bin/env bash
# seed - declare the broker topology and create the downstream table.
#
#   RabbitMQ : lab52.work exchange (direct) -> lab52.work queue
#              (x-dead-letter-exchange=lab52.dlx, x-max-length bound,
#               x-overflow=reject-publish), plus lab52.dlx -> lab52.dlq.
#   Redpanda : lab52.events topic (broker-family comparison only).
#   Postgres : processed_messages downstream table.
#
# The Go services declare the same RabbitMQ shape on startup, so these
# management-API calls are idempotent (matching arguments => no error).
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"
load_env

MGMT="http://localhost:${LAB_RABBITMQ_MGMT_PORT:-15672}"
AUTH="guest:guest"
MAXLEN="${LAB_QUEUE_MAX_LEN:-50000}"
VH="%2F"  # default vhost "/"

echo "== RabbitMQ topology =="
curl -fsS -u "${AUTH}" -X PUT "${MGMT}/api/exchanges/${VH}/lab52.dlx" \
  -H 'content-type: application/json' \
  -d '{"type":"fanout","durable":true}' && echo "  exchange lab52.dlx ok"
curl -fsS -u "${AUTH}" -X PUT "${MGMT}/api/queues/${VH}/lab52.dlq" \
  -H 'content-type: application/json' \
  -d '{"durable":true}' && echo "  queue lab52.dlq ok"
curl -fsS -u "${AUTH}" -X POST "${MGMT}/api/bindings/${VH}/e/lab52.dlx/q/lab52.dlq" \
  -H 'content-type: application/json' -d '{"routing_key":""}' && echo "  binding dlx->dlq ok"

curl -fsS -u "${AUTH}" -X PUT "${MGMT}/api/exchanges/${VH}/lab52.work" \
  -H 'content-type: application/json' \
  -d '{"type":"direct","durable":true}' && echo "  exchange lab52.work ok"
curl -fsS -u "${AUTH}" -X PUT "${MGMT}/api/queues/${VH}/lab52.work" \
  -H 'content-type: application/json' \
  -d "{\"durable\":true,\"arguments\":{\"x-dead-letter-exchange\":\"lab52.dlx\",\"x-max-length\":${MAXLEN},\"x-overflow\":\"reject-publish\"}}" \
  && echo "  queue lab52.work ok (x-max-length=${MAXLEN}, overflow=reject-publish)"
curl -fsS -u "${AUTH}" -X POST "${MGMT}/api/bindings/${VH}/e/lab52.work/q/lab52.work" \
  -H 'content-type: application/json' -d '{"routing_key":"work"}' && echo "  binding work->work ok"

echo
echo "== Redpanda topic (broker-family comparison) =="
docker compose exec -T redpanda rpk topic create lab52.events --partitions 6 --replicas 1 2>/dev/null \
  && echo "  topic lab52.events created" || echo "  topic lab52.events already exists (ok)"
docker compose exec -T redpanda rpk topic list || true

echo
echo "== Postgres downstream table =="
docker compose exec -T postgres psql -U lab52 -d lab52 -v ON_ERROR_STOP=1 -c "
CREATE TABLE IF NOT EXISTS processed_messages (
  msg_id       TEXT PRIMARY KEY,
  msg_type     TEXT NOT NULL,
  consumer_id  TEXT NOT NULL,
  attempt      INT  NOT NULL,
  processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);" && echo "  table processed_messages ok"

echo
echo "Seed complete. Next: make brokers-status && make consumer-status"
