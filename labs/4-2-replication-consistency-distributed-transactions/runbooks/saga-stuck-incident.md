# Runbook — saga stuck (or 2PC in-doubt)

## Symptoms

- Grafana row "Saga + 2PC outcomes" shows place-order success rate
  collapsing.
- `twopc_in_doubt_count > 0` for any service, sustained.
- Customers report orders "paid but never shipped" or stock
  reservations that never released.

## Severity

- 2PC in-doubt: page immediately. Locks on hot rows can stall
  unrelated traffic.
- Saga stuck (a step keeps timing out and compensations are running):
  ticket; check the failing step's downstream first.

## First 5 minutes — confirm

1. `make ps` — every service healthy? If any is unhealthy, restart
   it and let the consumer's idempotency cover the retried events.
2. Pull the orchestrator's `place_order_failed_total{mode="..."}` rate
   in Prometheus. If `mode="2pc"` is the only one growing, the saga
   path is fine and the problem is the 2PC coordinator.
3. List in-doubt prepared transactions on each participant:
   ```sql
   SELECT * FROM pg_prepared_xacts;
   ```
   These are exactly the gids the orchestrator failed to deliver a
   commit/abort decision to.

## Diagnose

- Cross-reference each in-doubt gid against the `twopc_log` table on
  the same DB:
  ```sql
  SELECT * FROM twopc_log WHERE state = 'prepared' ORDER BY prepared_at DESC LIMIT 50;
  ```
  Any row with state='prepared' and prepared_at older than your
  commit-timeout window is in-doubt.
- For the saga path: check `events_outbox` rows where `published_at IS
  NULL` for an extended period. Outbox-relay may be stuck — check its
  logs and `outbox_publish_errors_total`.

## Mitigate

### 2PC in-doubt resolution

If the gid corresponds to a known committed sibling on another
participant: `COMMIT PREPARED 'gid'` on the laggard.
Otherwise: `ROLLBACK PREPARED 'gid'`. Do NOT split the decision —
either every participant commits or every one aborts.

```sql
COMMIT PREPARED 'lab42-...';
-- or
ROLLBACK PREPARED 'lab42-...';
```

Update twopc_log:
```sql
UPDATE twopc_log SET state = 'committed' /* or 'aborted' */, finished_at = now() WHERE gid = '...';
```

### Stuck saga step

If the step's downstream is back, the saga's retry loop will pick up
where it left off because the consumer is idempotent.
If the step's downstream is permanently lost, run the compensation
endpoint manually for the affected `order_id`s.

## Postmortem

- Was the in-doubt window observable on the dashboard before customer
  reports? If not, raise the alert threshold sensitivity.
- Should this operation move from 2PC to saga? Consult the lab
  evidence (section 4 of `docs/review.md`).
- Is there a watchdog that auto-aborts gids older than
  `commit_timeout`? Add one if no.
