-- Lab 5-3 schema. One Postgres holds everything because the lesson is
-- about the boundary between a local ACID write and an asynchronous
-- event, not about sharding.
--
--  * orders          - the producer's business truth (dual-write/outbox)
--  * events_outbox    - rows the Go outbox-relay tails and publishes
--  * processed_ids    - dedup table the idempotent consumer consults
--  * projection       - the read-model the consumer rebuilds from the log
--  * side_effects     - one row per external side effect that fired
--                       (count == external actions taken)

CREATE TABLE IF NOT EXISTS orders (
    order_id   TEXT PRIMARY KEY,
    last_seq   BIGINT NOT NULL DEFAULT 0,
    status     TEXT NOT NULL,
    amount     BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
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

-- Partial index so the relay's "WHERE published_at IS NULL ORDER BY id"
-- poll stays cheap as the table grows.
CREATE INDEX IF NOT EXISTS events_outbox_unpublished_idx
    ON events_outbox (id) WHERE published_at IS NULL;

CREATE TABLE IF NOT EXISTS processed_ids (
    event_id     TEXT PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS projection (
    order_id       TEXT PRIMARY KEY,
    last_seq       BIGINT NOT NULL DEFAULT 0,
    status         TEXT NOT NULL,
    amount         BIGINT NOT NULL DEFAULT 0,
    events_applied BIGINT NOT NULL DEFAULT 0,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS side_effects (
    id         BIGSERIAL PRIMARY KEY,
    order_id   TEXT NOT NULL,
    event_id   TEXT NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'notify',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS side_effects_event_idx ON side_effects (event_id);
