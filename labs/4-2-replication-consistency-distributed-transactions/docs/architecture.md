# Lab 4-2 Architecture

## Topology

```
                 ┌────────────┐         async
                 │  primary   │──streaming──► replica-1
                 │ (postgres) │──streaming──► replica-2
                 └─────┬──────┘
                       ▲
            writes     │     reads (per policy)
                       │
                  raw-bench / lag-sampler

      ┌──────────────────────────────────────────────────────┐
      │                       saga path                       │
      │                                                      │
      │  loadgen-saga  ───────►  orchestrator                 │
      │                              │                       │
      │  saga: chained POSTs  ───►  payment ─► payment-pg     │
      │                          ►  inventory ─► inventory-pg │
      │                          ►  shipping ─► shipping-pg   │
      │                                            │          │
      │                                       events_outbox   │
      │                                            │          │
      │                            outbox-relay ◄──┘          │
      │                                  │                    │
      │                              Redpanda (events / dlq)  │
      │                                  │                    │
      │                              consumer (idempotent|naive)
      └──────────────────────────────────────────────────────┘

      ┌──────────────────────────────────────────────────────┐
      │                       2PC path                       │
      │                                                      │
      │  orchestrator ──► /xa/prepare to all 3 (locks held)   │
      │                ◄──── ack                              │
      │                ──► /xa/commit | /xa/abort to all 3    │
      └──────────────────────────────────────────────────────┘
```

## Layers

- **Postgres cluster**: primary + 2 streaming replicas. Replication is
  async (the only kind that can demonstrate read-after-write tail
  latency under load). Quorum mode (synchronous_commit + sync_names)
  is the bonus path; toggling it is left to the student.
- **Per-service Postgres** (one each for payment, inventory, shipping):
  separate failure domains for the saga + 2PC participants. Each owns
  its own `events_outbox` and `processed_events` tables.
- **Redpanda**: Kafka-compatible broker. Topic `events` is partitioned
  by `order_id` to preserve per-order ordering for the saga. `dlq`
  exists for completeness and is not exercised in the default flow.
- **Go services**: payment-svc, inventory-svc, shipping-svc each
  expose saga endpoints (charge|reserve|schedule and their
  compensations) and 2PC endpoints (`/xa/prepare`, `/xa/commit`,
  `/xa/abort`) backed by Postgres `PREPARE TRANSACTION`.
- **Orchestrator**: routes `POST /place-order?mode=saga|2pc` to the
  right strategy. Reports per-mode success rate, latency, compensation
  count.
- **Outbox-relay**: tails the per-service `events_outbox`, publishes
  to Redpanda, marks the row published — all in one DB transaction so
  a crash leaves the row available for the next poll (at-least-once).
- **Consumer**: applies side effects to a read-model in either naive
  mode (replay-unsafe) or idempotent mode (UPSERT keyed on `event_id`
  in the same tx as the side effect).
- **Lag-sampler**: queries primary `pg_current_wal_lsn()` and each
  replica's `pg_last_wal_replay_lsn()` every 100ms; emits histograms
  for Prometheus and writes raw samples to `perf/results/lag/runN/`.
- **Raw-bench**: drives the read-after-write workload using one of the
  four `internal/readpolicy/` modes.
- **Loadgen-saga**: drives place-order via the orchestrator.
- **Fault-injector**: HTTP store keyed on service name with
  `latency`/`fail` modes; services poll it with a 200ms cache so
  faults propagate end-to-end in <1s.

## State machines

### Saga (place-order)

```
   START --(charge OK)--> PAID --(reserve OK)--> RESERVED --(schedule OK)--> COMPLETED
     |                       |                        |                          
     |                       |                        +--(schedule FAIL)--> CANCELLING
     |                       +--(reserve FAIL)--> RELEASING                       
     +--(charge FAIL)--> FAILED                                                   
   CANCELLING --(cancel OK + release OK + refund OK)--> FAILED
```

### 2PC (place-order)

```
   START --PREPARE--> PREPARED --COMMIT--> COMMITTED
                          |
                          +--ABORT--> ABORTED   (if any prepare failed)
                          |
                          +--(commit fails for any participant)--> IN-DOUBT
```

The IN-DOUBT state is the lab's pedagogical hot spot. The orchestrator
does NOT durably persist its commit decision (it only logs it); a
participant left holding a prepared transaction will keep the
underlying row locks until somebody manually issues `ROLLBACK
PREPARED` for that gid. Watch `twopc_in_doubt_count` on the dashboard.

## Build / runtime

One Go module (`github.com/hlsa2-labs/lab4-2`); one shared Dockerfile
that builds whichever `cmd/<bin>` the compose service requests via the
`BIN` build arg. This minimises image churn between services and lets
`make up --build` reuse the layer cache.
