#!/usr/bin/env bash
# clear-poison - purge the work + dead-letter queues and reset the
# producer's poison injection knob, so the next experiment starts clean.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"
load_env

MGMT="http://localhost:${LAB_RABBITMQ_MGMT_PORT:-15672}"
AUTH="guest:guest"

echo "[clear-poison] purging queues"
for q in lab52.work lab52.dlq; do
  curl -fsS -u "${AUTH}" -X DELETE "${MGMT}/api/queues/%2F/${q}/contents" \
    && echo "  purged ${q}" || echo "  could not purge ${q} (ok if empty/absent)"
done

echo "[clear-poison] resetting producer poison_count=0"
curl -fsS -X POST -H 'content-type: application/json' \
  -d '{"poison_count":0}' \
  "http://localhost:${LAB_PRODUCER_PORT:-8080}/admin/config" >/dev/null \
  && echo "  producer poison_count reset" || true

echo "[clear-poison] done."
