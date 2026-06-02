#!/usr/bin/env bash
# Seed the primary cluster + per-service DBs with predictable rows.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

log_step "seeding primary (lag/raw bench accounts)"
docker compose exec -T postgres-primary psql -U hlsa -d hlsa <<'SQL'
INSERT INTO accounts (session_id, balance, version)
SELECT 'seed-' || g::text, 0, 0
FROM generate_series(1, 100) AS g
ON CONFLICT (session_id) DO NOTHING;
SELECT count(*) AS accounts FROM accounts;
SQL

log_step "seeding payment + inventory + shipping DBs"
docker compose exec -T payment-pg psql -U payment -d payment <<'SQL'
INSERT INTO accounts (user_id, balance) SELECT 'user-' || g::text, 1000000 FROM generate_series(1, 50) AS g
ON CONFLICT (user_id) DO UPDATE SET balance = EXCLUDED.balance;
SELECT count(*) AS users FROM accounts;
SQL

docker compose exec -T inventory-pg psql -U inventory -d inventory <<'SQL'
INSERT INTO stock (sku, available, reserved) SELECT 'sku-' || g::text, 1000000, 0 FROM generate_series(1, 50) AS g
ON CONFLICT (sku) DO UPDATE SET available = EXCLUDED.available, reserved = EXCLUDED.reserved;
SELECT count(*) AS skus FROM stock;
SQL

docker compose exec -T shipping-pg psql -U shipping -d shipping <<'SQL'
SELECT count(*) AS shipments FROM shipments;
SQL

log_step "verifying replicas have caught up"
PRIMARY_LSN=$(docker compose exec -T postgres-primary psql -U hlsa -d hlsa -tAc "SELECT pg_current_wal_lsn()::text")
echo "primary LSN: ${PRIMARY_LSN}"
for r in postgres-replica-1 postgres-replica-2; do
  for i in 1 2 3 4 5 6 7 8 9 10; do
    R=$(docker compose exec -T "${r}" psql -U hlsa -d hlsa -tAc "SELECT pg_last_wal_replay_lsn()::text" 2>/dev/null || true)
    if [ -n "${R}" ]; then
      echo "  ${r} LSN: ${R}"
      break
    fi
    sleep 1
  done
done

echo
echo "seed: ok"
