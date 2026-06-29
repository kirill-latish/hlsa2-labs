# Runbook: dual-write inconsistency

> Page trigger: alert `OutboxBacklogGrowing`
> (`lab53:outbox_backlog > 1000 for 2m`), or a downstream report that
> business state changed but no event was ever seen.

## Symptoms

- **Orphaned state**: a row changed in the `orders` (business) table but
  the corresponding event never reached the `projection` / downstream
  consumers. Inventory is wrong, billing never fired, and it never
  self-heals - this is *permanent silent corruption*.
- Or, in outbox mode, a **growing outbox backlog**
  (`lab53:outbox_backlog`): events are committed but the relay is not
  shipping them.

## Quick triage: which failure is it?

- `make analyze-consistency LABEL=incident` counts orphaned state
  changes (orders with no projection row). Non-zero -> the producer is
  doing a **naive dual-write** (DB then publish as two steps) and a
  crash dropped the publish.
- A non-zero **outbox backlog** with **zero** orphans -> the atomic
  write worked but the **relay is stalled**.

## 1. If orphans exist: stop the naive dual-write (5 min)

- `make apply-fix CANDIDATE=outbox` - route writes through the outbox so
  the business row and the event commit in one local transaction.
- Reconcile the existing orphans: re-derive the missing events from the
  `orders` rows that have no matching `projection`/outbox row and insert
  them into `events_outbox` for the relay to ship.

## 2. If the relay is stalled: restart the relay (5 min)

- `make relay-status` - confirm `/healthz` and read the backlog.
- Check `outbox-relay` logs for produce errors
  (`lab53_outbox_publish_errors_total`). Common causes: broker
  unreachable, topic missing (`make seed`), or a poison row.
- Restart: `docker compose restart outbox-relay`. The relay is
  at-least-once and idempotent-safe, so replays are harmless.

## 3. Confirm consumers stay idempotent

- Outbox delivery is still **at-least-once** - the relay can republish.
  Consumers MUST be `idempotent-consumer`
  (`make apply-fix CANDIDATE=idempotent-consumer`), or fixing the
  dual-write just trades silent loss for double effects.

## 4. Validate

- `make analyze-consistency LABEL=incident-after` -> `orphaned_state_changes == 0`.
- `lab53:outbox_backlog` drains to ~0; `lab53_outbox_published_total` rises.

## 5. Postmortem

- The relay must preserve **commit order** (ships `events_outbox` in
  `id` order, keyed by `aggregate_id`). Verify a reorder did not break a
  per-entity invariant downstream.
- Add a lifecycle/cleanup job for published outbox rows so the table
  does not grow without bound.
