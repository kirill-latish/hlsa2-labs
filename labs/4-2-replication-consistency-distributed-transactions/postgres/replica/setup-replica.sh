#!/usr/bin/env sh
# Lab 4-2 replica bootstrap.
#
# On first start the data dir is empty: pg_basebackup the primary's
# state into it, write a .pgpass for the replicator user, and start
# Postgres as a hot standby.
#
# On subsequent starts the data dir already exists: just start Postgres.
#
# This script runs as the `postgres` user (set in compose).

set -eu

DATA_DIR=/var/lib/postgresql/data
: "${PRIMARY_HOST:?PRIMARY_HOST is required}"
: "${REPL_USER:=replicator}"
: "${REPL_PASSWORD:=replicator}"
: "${REPLICA_NAME:=replica}"

if [ ! -s "${DATA_DIR}/PG_VERSION" ]; then
  echo "[$(date -Iseconds)] [${REPLICA_NAME}] data dir empty - running pg_basebackup against ${PRIMARY_HOST}"

  # Wait for the primary to accept connections. The compose
  # healthcheck on postgres-primary is the main gate, but the inside-
  # container hostname only resolves once the network is up.
  for i in $(seq 1 60); do
    if pg_isready -h "${PRIMARY_HOST}" -p 5432 -U "${REPL_USER}" -d postgres -q; then
      break
    fi
    echo "[${REPLICA_NAME}] waiting for primary... (${i}/60)"
    sleep 1
  done

  PGPASSWORD="${REPL_PASSWORD}" pg_basebackup \
    --pgdata="${DATA_DIR}" \
    --host="${PRIMARY_HOST}" \
    --port=5432 \
    --username="${REPL_USER}" \
    --wal-method=stream \
    --checkpoint=fast \
    --progress \
    --write-recovery-conf \
    --slot="${REPLICA_NAME}_slot" \
    --create-slot \
    --verbose

  # Make sure standby.signal is present (pg_basebackup -R already
  # writes it for >=12, but be explicit).
  touch "${DATA_DIR}/standby.signal"
  chmod 700 "${DATA_DIR}"
fi

# Hand off by exec'ing postgres directly. We must mirror the primary's
# capacity-related GUCs so recovery can proceed: the standby refuses
# to start if max_connections / max_prepared_transactions /
# max_wal_senders / max_replication_slots are LOWER than on the
# primary at the WAL position we're starting from.
exec postgres \
  -c hot_standby=on \
  -c listen_addresses=* \
  -c max_connections=200 \
  -c max_prepared_transactions=64 \
  -c max_wal_senders=10 \
  -c max_replication_slots=10 \
  -c wal_level=replica
