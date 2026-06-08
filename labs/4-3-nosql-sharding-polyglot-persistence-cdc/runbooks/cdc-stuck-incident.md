# Runbook — CDC stuck (Debezium connector failed or backlogged)

## Detect

- Alert `CDCLagHigh` (p99 lag > 30s for 1m).
- `make debezium-status` shows the task in `FAILED` or `UNASSIGNED`.
- Grafana → CDC lag panel: `cdc.public.products` lag stuck.
- Postgres `pg_replication_slots.confirmed_flush_lsn` no longer advancing
  while clients keep writing — slot is going to grow forever.

## Triage (< 5 min)

1. `bash scripts/debezium-status.sh` — read the task `state` and `trace`.
2. `docker compose logs --tail=200 debezium-connect | tail -80` — usually
   tells you whether it's an ES connectivity problem (es-consumer) or a
   Postgres replication-slot problem.
3. `docker compose exec postgres psql -U hlsa -d hlsa -c
   "SELECT slot_name, active, restart_lsn, confirmed_flush_lsn FROM pg_replication_slots;"`
   — the gap between `pg_current_wal_lsn()` and `confirmed_flush_lsn` is
   your unflushed backlog.

## Mitigate (next 15 min)

- **Connector failed but task is stuck**:

  ```bash
  curl -X POST http://localhost:18083/connectors/lab43-postgres/restart
  curl -X POST http://localhost:18083/connectors/lab43-postgres/tasks/0/restart
  ```

- **Connector config drifted**:

  ```bash
  bash debezium/register.sh   # idempotent; PUTs the canonical config
  ```

- **Slot is full / long-running transaction blocking it**:
  identify the blocking xact (`SELECT pid, query, query_start FROM
  pg_stat_activity WHERE state='idle in transaction' ORDER BY query_start;`),
  decide to commit or kill, then verify the slot drains.

## Post-incident

- Backfill the derived store if the gap was longer than your retention:
  drop the affected es indexes, restart the connector with `snapshot.mode=initial`,
  let `es-consumer` repopulate.
- Add a note to `docs/review.md` about how the freshness policy behaved
  during the incident (lsn-wait should have started falling back to SoR;
  read-from-derived should have shown elevated violations).
