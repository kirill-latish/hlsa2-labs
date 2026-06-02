# Runbook — replica lag incident

## Symptoms

- Grafana `Consistency Overview` row "Replica lag distribution" shows
  p99 lag > 1s for one or more replicas, sustained for >1 minute.
- Read-after-write violation rate spikes on dependant services.
- Alert `ReplicaLagHigh` fires.

## Severity & ownership

- Severity: ticket if a single replica; page if both replicas (read
  capacity halved).
- Pri owner: storage on-call.

## First 5 minutes — confirm and contain

1. Compare `replica_lag_bytes_current` vs `replica_lag_seconds_current`
   in Grafana. A growing bytes-behind that maps to a flat
   seconds-behind = replica is replaying as fast as it can but the
   primary's WAL rate is higher.
2. Check primary write rate: `pg_stat_database` `tup_inserted +
   tup_updated + tup_deleted`. Confirm it spiked.
3. If a single-row mistake (mass UPDATE etc.) caused the spike, kill
   that statement before it generates more WAL.
4. Page the read-after-write owners: any service that uses
   `MODE=session-pin` or naive replica reads should know its tail
   widened.

## 5–30 minutes — diagnose

- Replica's `pg_stat_replication.write_lag`, `flush_lag`, `replay_lag`
  on the primary. Replay_lag growing means the replica's redo is the
  bottleneck (CPU/disk on the standby), not the network.
- `pg_stat_activity` on the standby for long-running queries blocking
  redo (autovacuum on standby is the usual culprit). Cancel them.
- If WAL backlog has overflowed `wal_keep_size`, the replica may have
  fallen out of streaming and be catching up via archive — check
  `pg_replication_slots.active`.

## 30+ minutes — mitigate

- Drain reads off the lagging replica: temporarily switch all
  read-after-write consumers to `MODE=primary-read` until lag
  recovers. The throughput cost is described in
  [docs/review.md](../docs/review.md) section 3.
- If the replica is stuck and re-syncing won't catch up before the
  business window ends: take it out of rotation and rebuild from
  primary via `pg_basebackup`.

## Postmortem checklist

- Did `wal_keep_size` cover the burst? If not, raise it or move to
  replication slots.
- Did the read policy match the measured tail (per topic 248 step 4)?
  If a session-pin TTL was shorter than the observed p99.9, document
  the change and the new evidence.
- Add a synthetic write-burst alert if the lag was triggered by a
  predictable batch job.
