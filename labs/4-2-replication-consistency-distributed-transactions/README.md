# HLSA2 Lab 4-2 — Replication, Consistency, and Distributed Transactions

Companion lab for **topic 248**. You will:

1. Stand up a streaming-replicated Postgres cluster (1 primary, 2
   async replicas) plus three saga participants on their own
   Postgres instances, plus Redpanda for events.
2. Measure the **replica lag distribution** under a steady write
   workload — not the mean, the tail.
3. Reproduce a **read-after-write violation** on naive replica reads
   and prove that one of the four pre-wired fixes drives it to zero.
4. Benchmark **2PC vs saga** under an injected fault — same business
   semantics, very different blast radius.
5. Prove **saga idempotency** by replaying the same event window
   twice and asserting the consumer's final state hash is identical.
6. Apply one targeted improvement and decide whether the change is
   real or within 2-sigma noise.
7. Write the architecture review and runbooks.

> Like every lab in this course, the rule is **ship consistency
> changes with evidence, not intuition**. Every claim in
> `docs/review.md` must cite an artifact under `perf/results/` or
> `docs/img/`.

## Prerequisites

- Docker + Docker Compose (Docker Desktop or equivalent).
- `make`, `bash`, `jq`, `python3` (3.10+), `wget` on the host.
- ~6 GB free disk for images + Postgres TSDB volumes.
- Ports `3000`, `5432–5434`, `5440–5442`, `8080–8083`, `9000`, `9090`,
  `9092`, `9101–9103`, `9644` free on the host (override via `.env`).

## Stack overview

| Service              | Port      | Role                                                   |
| -------------------- | --------- | ------------------------------------------------------ |
| `postgres-primary`   | 5432      | Source of truth for replication experiments            |
| `postgres-replica-1` | 5433      | Async streaming replica                                |
| `postgres-replica-2` | 5434      | Async streaming replica                                |
| `payment-pg`         | 5440      | Payment service's own DB                               |
| `inventory-pg`       | 5441      | Inventory service's own DB                             |
| `shipping-pg`        | 5442      | Shipping service's own DB                              |
| `redpanda`           | 9092/9644 | Kafka-compatible broker (events, dlq topics)           |
| `payment-svc`        | 8081      | Saga + 2PC participant: `/charge`, `/refund`, `/xa/*`  |
| `inventory-svc`      | 8082      | Saga + 2PC participant: `/reserve`, `/release`, `/xa/*` |
| `shipping-svc`       | 8083      | Saga + 2PC participant: `/schedule`, `/cancel`, `/xa/*`|
| `orchestrator`       | 8080      | `POST /place-order?mode=saga\|2pc`                     |
| `outbox-relay`       | 9102      | Tails per-service outbox -> Redpanda                   |
| `consumer`           | 9103      | Idempotent or naive consumer (replay test)             |
| `lag-sampler`        | 9101      | Samples replica LSN every 100ms                        |
| `fault-injector`     | 9000      | HTTP fault store keyed on `service`                    |
| `prometheus`         | 9090      | Scrapes everything every 5s                            |
| `grafana`            | 3000      | Provisioned `Consistency Overview` dashboard           |

All Go binaries live under one `go.mod`; the shared
[Dockerfile](Dockerfile) builds whichever `cmd/<name>` compose asks
for via the `BIN` build arg. Architecture details in
[docs/architecture.md](docs/architecture.md).

## One-line bring-up

```bash
cp .env.example .env
make up
make seed
make topics
make env-fingerprint
```

Verify: `make ps` should show every container running/healthy and
Grafana at <http://localhost:3000> should load the
**Consistency Overview** dashboard with live data within ~2 minutes.

## Steps from the topic guide

| Step | Command(s)                                                                                       |
| ---- | ------------------------------------------------------------------------------------------------ |
| 1    | `make up`, `make ps`, `make seed`, `make topics`                                                 |
| 2    | `make env-fingerprint`                                                                           |
| 3    | `make bench-lag RUNS=3 DURATION=5m`, `make analyze-lag`                                          |
| 4    | `make bench-raw MODE=naive`, `make bench-raw MODE=session-pin`, `make compare-raw BEFORE=naive AFTER=session-pin` |
| 5    | `make bench-2pc RUNS=3 LABEL=healthy`, `make bench-saga RUNS=3 LABEL=healthy`, `make inject-fault SERVICE=inventory MODE=latency P99_MS=2000`, `make bench-2pc RUNS=3 LABEL=faulted`, `make bench-saga RUNS=3 LABEL=faulted`, `make compare-2pc-saga`, `make clear-fault SERVICE=inventory` |
| 6    | `make seed-events WINDOW=24h`, `make replay WINDOW=24h CONSUMER_MODE=idempotent`, `make assert-idempotent CONSUMER_MODE=idempotent`, `make assert-idempotent CONSUMER_MODE=naive`, `make inject-fault SERVICE=shipping MODE=fail`, `make bench-saga RUNS=1 LABEL=compensation-test`, `make clear-fault SERVICE=shipping` |
| 7    | `make regression CANDIDATE=lsn-wait-on-raw RUNS=3`, `make analyze CANDIDATE=lsn-wait-on-raw`     |
| 8    | Fill `docs/review.md` from `docs/review.template.md`                                             |
| 9    | `make check-submission`                                                                          |

Each `make analyze-*` writes a Markdown / JSON report next to the
artifacts the review template tells you how to cite.

## Read-after-write modes

The four modes the topic guide references live in
[`internal/readpolicy/`](internal/readpolicy/). The `raw-bench`
binary instantiates one per worker.

- **`MODE=naive`** — random replica, no coordination.
- **`MODE=session-pin`** — reads from primary for `T_pin` ms after
  this *session* wrote.
- **`MODE=lsn-wait`** — capture the writer's LSN; ask each replica
  `pg_last_wal_replay_lsn()`; pick the first that has caught up; brief
  wait then primary fallback.
- **`MODE=primary-read`** — reads of any *entity* written within
  `T_pin` ms globally go to primary (coarser than session-pin).

## Fault injection

Same pattern as labs 3-2 / 3-3 but keyed on `service` and `mode`:

```bash
make inject-fault SERVICE=inventory MODE=latency P99_MS=2000
make inject-fault SERVICE=shipping  MODE=fail
make clear-fault  SERVICE=inventory
```

The injected service polls the fault store every 200ms (cached) so
new specs propagate end-to-end in <1s.

## Observability

- Prometheus UI: <http://localhost:9090>
- Grafana: <http://localhost:3000> (anonymous Admin)
- Provisioned dashboard: **Consistency Overview** — replica lag
  percentiles, place-order success rate + p99 by mode, saga step
  counters, 2PC in-doubt count, outbox publish rate, consumer
  events processed.

## What to submit

After the runs you should have:

- `docs/review.md` (filled from `docs/review.template.md`).
- `perf/results/env.txt`, `perf/results/meta.json`.
- Every `perf/results/...` directory cited in the review.
- Two Grafana screenshots in `docs/img/`:
  - `lag-overview.png` — the lag percentiles row at p99.9.
  - `twopc-in-doubt.png` — the in-doubt panel during the faulted 2PC run.
- Annotated runbooks (`runbooks/replica-lag-incident.md`,
  `runbooks/saga-stuck-incident.md`).

`make check-submission` parses `docs/review.md`, asserts every cited
filename exists, and warns on remaining `TODO` markers.

## Troubleshooting

- **Replicas won't start**: the entrypoint `pg_basebackup`s on first
  boot. If the primary's healthcheck is slow, replicas may retry for
  ~60s. Check `docker compose logs postgres-replica-1`.
- **`make bench-raw MODE=lsn-wait` is slower than naive**: that's the
  point — the lsn-wait coordination call adds round-trips. The
  trade-off is documented in section 3 of the review template.
- **Grafana shows "no data"**: give it 90s after `make bench-*` so
  the recording rules have a 5m window of samples.
- **Port collisions**: `cp .env.example .env` then change the
  conflicting `LAB_*_PORT`.
- **2PC in-doubt rows after a crash**: see
  [`runbooks/saga-stuck-incident.md`](runbooks/saga-stuck-incident.md).
