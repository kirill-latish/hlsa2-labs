-- Lab 5-1 system of record. A single key/value table with a monotonic
-- version column. Writers bump `version` and `updated_at`; the cache
-- stores an envelope carrying the version it cached, so the staleness
-- race can compare cached.version against the SoR version.

CREATE TABLE IF NOT EXISTS cache_items (
    key        TEXT        PRIMARY KEY,
    value      TEXT        NOT NULL,
    version    BIGINT      NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS cache_items_updated_idx ON cache_items (updated_at);
