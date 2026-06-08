# Lab 4-3 architecture

## Topology

```
                   +---------------------+
                   |  Postgres (SoR)     |
                   |  pgoutput logical   |
                   +----------+----------+
                              |
                              | WAL stream
                              v
                   +---------------------+        +-------------------+
                   |  Debezium Connect   +------->|     Redpanda      |
                   |  lab43-postgres     |        | cdc.public.*      |
                   +---------------------+        +---------+---------+
                                                            |
                              +-----------------------------+
                              |
                              v
                   +---------------------+        +---------------------+
                   |     es-consumer     +------->|  Elasticsearch      |
                   |  (group: lab43-es)  |        |  products / orders  |
                   +---------------------+        +---------------------+

   loadgen ------+--> Postgres (orders)
                 +--> mongos-{1,2} ---> mongo-shard-{1,2,3}

   bench-skew      -> mongos + per-shard server-status
   bench-cdc-lag   -> Postgres write -> ES read polling
   bench-polyglot  -> Postgres write + freshness-policy read

   fault-injector keyed on (slot, entity, weight)
   partition-stats scrapes per-shard server-status -> Prometheus
```

## Sharding

- **3 shards × single-mongod**: keeps the cluster portable on a laptop while
  still exercising mongos routing and chunk distribution.
- **Pre-sharded collections** (in `lab43` db) — the loadgen picks one per run
  via `SHARD_KEY`:

  | collection             | shard key                 | strategy        |
  |------------------------|---------------------------|-----------------|
  | `events_candidate`     | `{tenant_id: 1}`          | candidate       |
  | `events_hash_suffix`   | `{tenant_partition: 1}`   | hash-suffix     |
  | `events_composite`     | `{tenant_id: 1, user_hash: 1}` | composite-key |
  | `events_resharded`     | `{user_hash: "hashed"}`   | resharded       |

  We pre-shard all four up-front so `make apply-fix` is a routing change in
  the loadgen, not a rebalancing operation. This keeps the apply/measure
  cycle deterministic.

## CDC pipeline

- Postgres `wal_level=logical`, `max_replication_slots=10`, publication
  `lab43_pub` covers `users`, `products`, `orders`.
- Debezium connector `lab43-postgres` uses the `pgoutput` plugin and the
  ExtractNewRecordState transform with `add.fields=op,source.lsn,source.ts_ms,source.txId`.
- Topics: `cdc.public.users`, `cdc.public.products`, `cdc.public.orders`.
- `es-consumer` decodes the envelope (`internal/cdc`), normalises to the
  index shape, stamps `_indexed_at` + `_lsn`, and indexes by row PK.
- The consumer also exposes the highest indexed LSN at `/indexed-lsn` —
  this is what the lsn-wait freshness policy polls.

## Observability

- Prometheus scrapes `loadgen`, `es-consumer`, and `partition-stats`. The
  recording rule `lab43:max_to_mean_ratio` and the CDC-lag percentiles drive
  the **Polyglot Overview** dashboard in Grafana.
- Per-shard server-status (`metrics.commands.insert.total`, `extra_info.user_time_us`,
  `wiredTiger.cache.bytes currently in the cache`) is fanned out to
  Prometheus by `partition-stats`.

## Read path & freshness

- `read-from-sor` — strong consistency, all reads hit Postgres.
- `read-from-derived` — fast reads from ES, never blocking; the bench reports
  the violation rate (rows visible in derived but stale w.r.t. the just-issued
  write).
- `lsn-wait` — capture `pg_current_wal_lsn()` at write time, poll the
  consumer's indexed LSN with a bounded budget (`LSN_WAIT_MAX_MS`), fall back
  to SoR if the consumer doesn't catch up. Mirrors lab 4-2's lsn-wait but
  for the CDC pipeline.

## Failure isolation

- The fault-injector lets a single run reproduce a "celebrity tenant"
  (the loadgen polls every 200ms; an entity + weight pair tilts the
  Zipfian draw toward that tenant).
- `partition-stats` keeps reporting per-shard metrics during outages so
  the dashboard's max/mean panel still tells you which shard is sweating.
