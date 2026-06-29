#!/usr/bin/env bash
# seed - prepare the system of record and warm the cache:
#   1. ensure the cache_items table exists (init.sql also creates it)
#   2. insert KEYSPACE rows (item-0 .. item-(KEYSPACE-1))
#   3. ask the app to warm an initial hot set into Redis (read-through)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
cd "${LAB_ROOT}"

KEYSPACE="${KEYSPACE:-10000}"
WARM="${WARM:-2000}"

echo "[seed] 1) ensuring schema + inserting ${KEYSPACE} rows into Postgres"
docker compose exec -T postgres psql -U hlsa -d hlsa -v ON_ERROR_STOP=1 <<SQL
CREATE TABLE IF NOT EXISTS cache_items (
    key        TEXT        PRIMARY KEY,
    value      TEXT        NOT NULL,
    version    BIGINT      NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO cache_items (key, value, version)
SELECT 'item-' || g, 'value-' || g, 1
FROM generate_series(0, ${KEYSPACE} - 1) g
ON CONFLICT (key) DO NOTHING;
SQL

count="$(docker compose exec -T postgres psql -U hlsa -d hlsa -tAc 'SELECT count(*) FROM cache_items')"
echo "[seed] cache_items rows: ${count}"

echo "[seed] 2) warming ${WARM} keys into Redis via the app read-through path"
curl -fsS -X POST "${APP}/admin/warm?count=${WARM}" || {
  echo "[seed] WARNING: warm request failed (is the app healthy?)"; exit 1; }
echo

echo "[seed] complete. Verify shard placement with: make cluster-status"
