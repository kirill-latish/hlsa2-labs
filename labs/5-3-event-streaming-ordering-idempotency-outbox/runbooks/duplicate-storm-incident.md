# Runbook: duplicate storm

> Page trigger: alert `DuplicateStorm`
> (`lab53:duplicate_rate:1m > 0.5 for 1m`).

## Symptoms

- The duplicate rate (`lab53:duplicate_rate:1m`, suppressed duplicates /
  events consumed) jumps from its low-but-nonzero baseline toward 1.
- `lab53_duplicate_suppressed_total` climbs steeply on the
  idempotent consumers.
- If consumers are **not** idempotent, you instead see external side
  effects outrunning unique events (double charges / double emails) -
  this is the unprotected version of the same incident.

A healthy at-least-once pipeline always has *some* duplicates. A storm
means redelivery is happening far more than normal.

## 1. Confirm it is redelivery, not load (2 min)

- Check `lab53:events_produced:rate1m` vs `lab53:events_consumed:rate1m`.
  If consumed >> produced, the broker is redelivering committed records.
- Look for a consumer crash/restart loop: `docker compose ps` and the
  consumer logs. A consumer that dies before committing offsets will
  reprocess its in-flight batch on every restart.

## 2. Stop the redelivery source (5 min)

- If a consumer is crash-looping, fix the crash (or scale it out of the
  group) so offsets get committed after durable processing.
- If the redelivery is broker-driven (session timeout / rebalance
  storm), raise the consumer's `max.poll.interval` / processing budget
  so a slow batch is not mistaken for a dead member.

## 3. Confirm idempotency is actually protecting effects (2 min)

- `make verify-exactly-once LABEL=incident` - `side_effects_total` must
  equal `unique_events`. If it does not, the consumers are in
  `naive-consumer` mode: `make apply-fix CANDIDATE=idempotent-consumer`.
- The dedup marker and the side effect must commit in one transaction.
  A dedup check that commits separately from the effect re-opens the
  double-apply window.

## 4. Validate

- `lab53:duplicate_rate:1m` falls back toward the baseline from
  `perf/results/baseline/report.md`.
- `lab53_side_effects_total` stops outrunning unique events.

## 5. Postmortem

- Was the dedup window (`processed_ids` retention) longer than the
  maximum possible redelivery delay? A late redelivery past the window
  slips through dedup.
- Add the crash-and-duplicate scenario to the lab's
  `verify-exactly-once` comparison and cite the before/after artifact.
