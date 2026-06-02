-- Shipping service schema. See payment/init.sql for the pattern.

CREATE TABLE IF NOT EXISTS shipments (
    order_id    TEXT PRIMARY KEY,
    address     TEXT NOT NULL,
    status      TEXT NOT NULL,
    scheduled_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    cancelled_at TIMESTAMPTZ
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
