# Lab 5-3 - Event Streaming: Ordering, Idempotency, Outbox, Replay

A working Docker Compose stack that models an event-driven order
pipeline so you can **measure** the four hard truths of event streaming
instead of trusting that events are inherently reliable:

1. At-least-once delivery produces duplicates; only an **idempotent
   consumer** turns that into an exactly-once *effect*.
2. Ordering is guaranteed only **within a partition**, so the
   **partition key** decides which events are mutually ordered.
3. A naive **dual-write** (DB then broker, two steps) silently corrupts
   state on a crash; the **outbox pattern** makes it one local atomic
   write plus an asynchronous relay.
4. The log lets you **replay to rebuild** a projection - but a naive
   replay re-fires every external side effect.

Every `make` target maps to a step in the topic-252 guide and snapshots
artifacts under `perf/results/<label>/` exactly like lab 3-3.

## Topology

| Service        | Role |
|----------------|------|
| `producer`     | Go. Emits order events to Redpanda. Control API (`/start`,`/stop`,`/state`,`/summary`) plus an admin plane (`/admin/config`, `/admin/arm-crash`). Knobs: partition-key strategy (`entity`/`wrong`), duplicate-injection rate, publish mode (`direct`/`naive-dualwrite`/`outbox`), and a one-shot crash-between-writes. |
| `redpanda`     | Single-node Kafka-compatible broker. Partition count is set at topic creation by `make seed` (`REDPANDA_PARTITIONS`). |
| `consumer-1/2/3` | Go. One shared consumer group (`lab53-consumers`). Switchable semantics: `naive-consumer` (no dedup) vs `idempotent-consumer` (dedup by `event_id`, marker + effect in one tx). Detects ordering violations via per-entity sequence numbers, maintains the `projection` read-model and the `side_effects` counter, and can crash mid-batch on demand. |
| `outbox-relay` | Go. Tails `events_outbox` in commit order and publishes to Redpanda. **This is the lab's Debezium stand-in** (see below). |
| `postgres`     | `orders` (business truth), `events_outbox`, `processed_ids` (dedup), `projection` (read model), `side_effects` (external-effect counter). |
| `prometheus` + `grafana` | Pre-provisioned **Event Pipeline** dashboard: duplicate rate, ordering-violation rate, consumer lag (count + time), outbox backlog, offset/pipeline health, side effects. |

All services expose `lab53_*` Prometheus metrics through the shared
`internal/metrics` middleware (chi/v5 + promauto).

## Design decision: a Go outbox-relay instead of Debezium

The topic guide describes the outbox relay as "a Debezium relay". This
lab **deliberately implements the relay as a small Go poller**
(`cmd/outbox-relay`) rather than running a Debezium / Kafka-Connect
container. Rationale:

- **Robustness & reproducibility.** Debezium needs Kafka Connect, a
  connector plugin, logical-decoding/WAL configuration, and a
  registration call that is fragile across Postgres/Connect versions and
  slow to converge on `make up`. A Go poller tailing
  `events_outbox WHERE published_at IS NULL ORDER BY id` is deterministic
  and starts in seconds.
- **Same semantics that matter.** The relay preserves **commit order**
  (it ships rows in `id` order, keyed by `aggregate_id` so per-entity
  ordering survives), and it is **at-least-once** (a crash mid-publish
  simply replays the row). Those are exactly the two properties the lab
  reasons about, so the lesson is unchanged.
- **Honest about the trade-off.** A poller adds poll-interval latency
  and re-reads the table; Debezium's log-based CDC avoids that. The
  mechanism (atomic local write + asynchronous, order-preserving,
  at-least-once relay) is identical, which is what the review section on
  the outbox must argue. `make relay-status` checks this relay's health
  and backlog/lag.

## Quick start

```bash
cp .env.example .env
make up            # build + start everything
make seed          # create the topic with REDPANDA_PARTITIONS, verify schema
make ps
make env-fingerprint
make topic-status
make relay-status
```

Grafana: http://localhost:3000 (anonymous admin) -> "Event Pipeline".
Prometheus: http://localhost:9090.

## The experiments (and the commands)

### 1. Baseline + duplicate rate
```bash
make bench-baseline RUNS=3 DURATION=5m
make analyze-baseline
```
Reports throughput, consumer lag (count + time), and the duplicate rate
together. A flat-zero duplicate rate is suspicious; low-but-nonzero is
normal at-least-once behavior.

### 2. Idempotency under duplicates + crash
```bash
make apply-fix CANDIDATE=naive-consumer
make inject-duplicates RATE=20pct
make crash-consumer-midbatch
make verify-exactly-once LABEL=idempotency-naive   # side effects > unique events

make apply-fix CANDIDATE=idempotent-consumer
make inject-duplicates RATE=20pct
make crash-consumer-midbatch
make verify-exactly-once LABEL=idempotency-after   # side effects == unique events
make compare-idempotency BEFORE=idempotency-naive AFTER=idempotency-after
```

### 3. Ordering under wrong vs correct partition key
```bash
make bench-ordering KEY=wrong  DURATION=3m LABEL=ordering-wrong
make bench-ordering KEY=entity DURATION=3m LABEL=ordering-entity
make compare-ordering BEFORE=ordering-wrong AFTER=ordering-entity
```
`KEY=wrong` keys by `payment_method` (which varies per event), so an
order's events scatter across partitions and arrive out of order.
`KEY=entity` keys by `order_id`, so they stay ordered.

### 4. Dual-write inconsistency + the outbox fix
```bash
make apply-fix CANDIDATE=naive-dualwrite
make inject-crash-between-writes LABEL=dualwrite-naive
make analyze-consistency LABEL=dualwrite-naive     # orphaned state > 0

make apply-fix CANDIDATE=outbox
make inject-crash-between-writes LABEL=dualwrite-outbox
make analyze-consistency LABEL=dualwrite-outbox    # orphaned state == 0
make compare-consistency BEFORE=dualwrite-naive AFTER=dualwrite-outbox
```
Outbox delivery is still at-least-once, so the idempotent consumer from
step 2 is still required - the outbox fixes *atomicity*, not duplicates.

### 5. Replay to rebuild without re-firing side effects
```bash
make corrupt-projection
make replay-rebuild FROM=earliest MODE=rebuild-only LABEL=replay
make analyze-replay LABEL=replay
make verify-no-side-effects LABEL=replay
```
`rebuild-only` applies events to the projection but suppresses external
side effects; `MODE=reprocess` re-fires them (the footgun).

### 6. Review + submit
```bash
cp docs/review.template.md docs/review.md   # then fill the 7 sections
make check-submission                       # every cited artifact must exist
make down
```

## How the mechanisms are wired (for the curious)

- **Dedup**: `idempotent-consumer` inserts `event_id` into
  `processed_ids` `ON CONFLICT DO NOTHING`; a zero-row result means a
  duplicate, so the side effect is skipped. The marker insert, the
  projection update, and the side-effect insert all commit in **one
  transaction**, so a crash can't half-apply an event.
- **Ordering detection**: the `projection` row stores `last_seq`; an
  event whose `seq` is below the stored `last_seq` is counted as an
  ordering violation. Because the projection is shared across the three
  consumers, detection works regardless of which instance owns a
  partition.
- **Outbox atomicity**: in `outbox` mode the producer writes the
  `orders` row and the `events_outbox` row in one transaction; the relay
  ships the committed outbox row. The crash-between-writes injection
  therefore leaves **no orphan** in outbox mode but a guaranteed orphan
  in `naive-dualwrite` mode.
- **Replay**: `make replay-rebuild` truncates the projection, deletes
  the consumer group so it resets to the start of the log
  (`kgo.ConsumeResetOffset(AtStart)`), and restarts the consumers in
  `REPLAY_MODE=rebuild-only`. The rebuild duration (projection back to
  its pre-corruption row count) is the recovery-time metric.

## Layout

```
cmd/producer, cmd/consumer, cmd/outbox-relay   # three Go binaries (+ Dockerfiles)
internal/events     # the shared wire contract (Event, partition key)
internal/metrics    # lab53_* metrics + HTTP middleware
internal/outbox     # transactional-outbox helpers
internal/consumer   # Apply(): the dedup / ordering / replay semantics
postgres/init.sql   # schema
prometheus/         # scrape config + recording rules + alerts
grafana/            # provisioned datasource + Event Pipeline dashboard
scripts/            # one script per make target
perf/               # env.sh, workload.json, results/
runbooks/           # duplicate-storm + dual-write-inconsistency incidents
docs/               # review.template.md, img/
```
