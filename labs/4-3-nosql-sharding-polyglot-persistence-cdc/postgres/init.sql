-- Lab 4-3 system-of-record schema. CDC produces:
--   cdc.public.users
--   cdc.public.products
--   cdc.public.orders

CREATE TABLE IF NOT EXISTS users (
    id           BIGSERIAL PRIMARY KEY,
    tenant_id    TEXT        NOT NULL,
    email        TEXT        NOT NULL,
    display_name TEXT        NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS users_tenant_idx ON users (tenant_id);

CREATE TABLE IF NOT EXISTS products (
    id            BIGSERIAL PRIMARY KEY,
    tenant_id     TEXT          NOT NULL,
    sku           TEXT          NOT NULL,
    title         TEXT          NOT NULL,
    description   TEXT          NOT NULL,
    price_cents   BIGINT        NOT NULL,
    stock         INT           NOT NULL,
    search_facets TEXT[]        NOT NULL DEFAULT '{}',
    committed_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS products_tenant_idx ON products (tenant_id);
CREATE INDEX IF NOT EXISTS products_sku_idx    ON products (sku);

CREATE TABLE IF NOT EXISTS orders (
    id           BIGSERIAL PRIMARY KEY,
    tenant_id    TEXT        NOT NULL,
    user_id      BIGINT      NOT NULL,
    product_id   BIGINT      NOT NULL,
    quantity     INT         NOT NULL,
    total_cents  BIGINT      NOT NULL,
    status       TEXT        NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS orders_tenant_idx ON orders (tenant_id);
CREATE INDEX IF NOT EXISTS orders_user_idx   ON orders (user_id);

-- pgoutput logical decoding requires REPLICA IDENTITY for non-PK columns to
-- be present in UPDATE/DELETE events.
ALTER TABLE users    REPLICA IDENTITY FULL;
ALTER TABLE products REPLICA IDENTITY FULL;
ALTER TABLE orders   REPLICA IDENTITY FULL;

-- Publication consumed by the Debezium connector.
DROP PUBLICATION IF EXISTS lab43_pub;
CREATE PUBLICATION lab43_pub FOR TABLE users, products, orders;

-- Convenience view used by the freshness-policy reads.
CREATE OR REPLACE VIEW current_lsn AS
    SELECT pg_current_wal_lsn() AS lsn;

-- Replication user used by Debezium.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium') THEN
        CREATE ROLE debezium WITH REPLICATION LOGIN PASSWORD 'debezium';
    END IF;
END
$$;

GRANT USAGE   ON SCHEMA public TO debezium;
GRANT SELECT  ON ALL TABLES IN SCHEMA public TO debezium;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO debezium;
