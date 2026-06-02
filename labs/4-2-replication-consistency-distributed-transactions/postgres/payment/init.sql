-- Payment service schema.
--
-- The pattern is the same in every saga participant DB:
--  * one business table (`payments`) holding the local truth
--  * `events_outbox` - rows the outbox-relay tails and publishes
--  * `processed_events` - dedupe table the idempotent consumer
--    consults before applying side effects
--  * `twopc_log` - record of each prepared 2PC transaction so
--    in-doubt windows are observable

CREATE TABLE IF NOT EXISTS payments (
    order_id   TEXT PRIMARY KEY,
    amount     BIGINT NOT NULL,
    status     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS accounts (
    user_id    TEXT PRIMARY KEY,
    balance    BIGINT NOT NULL DEFAULT 1000000
);

CREATE TABLE IF NOT EXISTS events_outbox (
    id            BIGSERIAL PRIMARY KEY,
    event_id      TEXT UNIQUE NOT NULL,
    aggregate_id  TEXT NOT NULL,
    event_type    TEXT NOT NULL,
    payload       JSONB NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS events_outbox_unpublished_idx
    ON events_outbox (id) WHERE published_at IS NULL;

CREATE TABLE IF NOT EXISTS processed_events (
    event_id     TEXT PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS twopc_log (
    gid         TEXT PRIMARY KEY,
    order_id    TEXT NOT NULL,
    state       TEXT NOT NULL,
    prepared_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);

INSERT INTO accounts (user_id, balance)
SELECT 'user-' || g::text, 1000000
FROM generate_series(1, 1000) AS g
ON CONFLICT DO NOTHING;
