-- Lab 4-2 primary bootstrap.
--
-- Creates:
--  * replicator role used by the streaming replicas
--  * `accounts` table - the read-after-write target row set
--  * `orders` table  - the write workload target
--  * a tiny seed of accounts so the lag sampler's first reads have
--    something to find.

CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD 'replicator';

CREATE TABLE IF NOT EXISTS accounts (
    session_id  TEXT PRIMARY KEY,
    balance     BIGINT NOT NULL DEFAULT 0,
    version     BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS orders (
    id          BIGSERIAL PRIMARY KEY,
    payload     TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed: 100 accounts with random session ids so the read-after-write
-- bench has well-distributed targets out of the box.
INSERT INTO accounts (session_id, balance)
SELECT
    'seed-' || g::text,
    0
FROM generate_series(1, 100) AS g;
