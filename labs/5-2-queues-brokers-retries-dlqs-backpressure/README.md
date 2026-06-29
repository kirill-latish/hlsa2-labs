# HLSA2 Lab 5-2 — Queues, Brokers, Retries, DLQs, and Backpressure

Companion lab for topic 251. You will measure an async pipeline with
**evidence, not intuition**:

1. Stand up a Go **producer**, **RabbitMQ** (primary broker for
   retry/DLQ semantics) plus **Kafka/Redpanda** (alongside for
   broker-family comparison), a 3-instance **consumer fleet**, a
   **Postgres** downstream, an in-cluster **loadgen** controller, and a
   provisioned **Prometheus + Grafana** pair.
2. Measure a healthy baseline under sustained load, reporting **lag as
   both count and time** (the pair throughput-only reports hide).
3. Inject a single **poison message** and watch the fleet collapse under
   unbounded retries.
4. Apply **bounded retries + DLQ** and prove the collapse is
   structurally fixed.
5. **Classify** transient vs permanent failures and route each
   correctly under fault injection.
6. Drive sustained **2x overload** and show whether **backpressure**
   propagates and stabilizes lag, or doesn't and lets lag grow
   unboundedly.

> The consumer fleet defaults to **unbounded-retry** so step 3's failure
> is real and step 4's fix is real. `make apply-fix` flips the semantics
> at runtime (via each consumer's `/admin/config`) — no container
> restart. `.env` is for host port overrides only.

## Prerequisites

- Docker + Docker Compose (Docker Desktop or equivalent).
- `make`, `bash`, `jq`, `python3` (3.10+), `curl` on the host.
- ~4 GB of free disk for images + broker/Prometheus volumes.
- Ports `3000`, `5432`, `5672`, `8080`, `8090`, `8101-8103`, `9090`,
  `9092`, `9644`, `15672` free on the host (override via `.env`).

## Stack overview

| Service        | Port              | Role                                                    |
| -------------- | ----------------- | ------------------------------------------------------- |
| `producer`     | 8080              | Publishes to the active broker; fault-injection surface |
| `rabbitmq`     | 5672 / 15672      | Primary broker (work queue + DLX/DLQ, bounded queue)    |
| `redpanda`     | 9092 / 9644       | Kafka-API broker for broker-family comparison           |
| `consumer-1/2/3`| 8101 / 8102 / 8103 | Consumer fleet; flippable retry semantics              |
| `postgres`     | 5432              | Simulated downstream (latency + write)                  |
| `loadgen`      | 8090              | In-cluster controller; drives producer + fault knobs    |
| `prometheus`   | 9090              | Scrapes producer + consumers every 5s                   |
| `grafana`      | 3000              | Provisioned **Pipeline Overview** dashboard (anon Admin)|

All Go services share one `go.mod` at the lab root and the helpers under
[`internal/`](internal/) (`metrics`, `pipeline`).

## One-line bring-up

```bash
cp .env.example .env
make up
make seed
make env-fingerprint
make brokers-status
make consumer-status
```

`make seed` declares the RabbitMQ topology (the Go services also declare
it idempotently on startup), creates the Redpanda `lab52.events` topic,
and creates the Postgres `processed_messages` table.

## Steps from the topic guide

| Step | Command(s)                                                                                          |
| ---- | --------------------------------------------------------------------------------------------------- |
| 1    | `make up`, `make ps`, `make seed`, `make brokers-status`, `make consumer-status`                    |
| 2    | `make env-fingerprint`                                                                              |
| 3    | `make bench-baseline RUNS=3 DURATION=5m`, `make analyze-baseline`                                   |
| 4    | `make inject-poison COUNT=1 LABEL=poison-baseline DURATION=3m`, `make analyze-poison LABEL=poison-baseline`, `make clear-poison` |
| 5    | `make apply-fix CANDIDATE=bounded-retry MAX_RETRIES=5`, re-inject `poison-after`, `make compare-poison BEFORE=poison-baseline AFTER=poison-after` |
| 6    | `make apply-fix CANDIDATE=classify-failures`, `make bench-faults TRANSIENT_RATE=10pct PERMANENT_RATE=2pct DURATION=5m LABEL=faults`, `make analyze-faults LABEL=faults` |
| 7    | `make apply-fix CANDIDATE=backpressure-signal`, `make bench-backpressure RATE=2x DURATION=10m LABEL=backpressure`, `make analyze-backpressure LABEL=backpressure` |
| 8    | Fill in `docs/review.md` from `docs/review.template.md`                                             |
| 9    | `make check-submission`                                                                             |

Each `make analyze-*` writes a Markdown report under
`perf/results/<label>/report.md` that the review template tells you how
to cite.

## How fault injection works

Faults are expressed end-to-end by **message type**. The producer stamps
each message; the consumer simulates the matching downstream outcome:

- `normal` — succeeds after a simulated Postgres write.
- `poison` — unprocessable; always fails; classified **permanent**.
- `transient` — fails the first `TRANSIENT_RECOVER_AFTER` (default 2)
  attempts, then succeeds (flaky downstream: 503 / timeout / lock).
- `permanent` — always fails; classified **permanent** (400 / validation
  / dangling reference).

The producer's `/admin/config` knobs (driven by `make` via loadgen):
`poison_count`, `transient_rate`, `permanent_rate`,
`overload_multiplier`, `backpressure`.

## Consumer retry semantics (`make apply-fix`)

Flipped at runtime by POSTing `/admin/config` to each consumer:

- **`unbounded-retry`** (default) — nack + requeue forever. One poison
  message wedges a consumer slot indefinitely. This is the trap step 3
  reproduces.
- **`bounded-retry`** — track retry count per message; exponential
  backoff with ±20% jitter (≈1s, 2s, 4s, 8s, 16s); route to the DLQ
  after `MAX_RETRIES`. The retry/backoff happens off the consume path
  (delayed republish), so the slot frees immediately.
- **`classify-failures`** — permanent failures dead-letter immediately
  (no wasted retries); transient failures take the bounded-retry path.
- **`backpressure-signal`** — bounded-retry plus broker-level
  backpressure: the work queue is bounded (`x-max-length` +
  `x-overflow=reject-publish`) and the producer (with publisher
  confirms) honors rejections by slowing.

## Metrics (`lab52_*`)

| Metric                                       | Meaning                                  |
| -------------------------------------------- | ---------------------------------------- |
| `lab52_messages_produced_total{broker,type}` | Messages published                       |
| `lab52_messages_acked_total{consumer}`       | Per-consumer throughput (rate)           |
| `lab52_consumer_lag_count`                   | Messages waiting (lag — count)           |
| `lab52_oldest_unprocessed_age_seconds{consumer}` | Age of oldest unprocessed (lag — time)|
| `lab52_retries_total{consumer}`              | Retry attempts                           |
| `lab52_dlq_total{consumer}`                  | Dead-lettered messages                   |
| `lab52_producer_errors_total{reason}`        | Publish rejections (backpressure signal) |
| `lab52_processing_duration_seconds{consumer}`| Processing latency (p50/p99)             |

## Observability

- Prometheus UI: <http://localhost:9090>
- Grafana: <http://localhost:3000> (anonymous Admin)
- Provisioned dashboard: **Pipeline Overview** — lag (count + time),
  per-consumer throughput + processing p50/p99, retry/DLQ rate, producer
  error rate, produced mix, active mode.

## What to submit

- `docs/review.md` (filled from `docs/review.template.md`).
- `perf/results/env.txt` + `perf/results/meta.json`.
- Every `perf/results/<label>/` run directory cited in the review.
- Grafana screenshots under `docs/img/` cited from the review.
- The two runbooks under `runbooks/`.

`make check-submission` parses `docs/review.md`, asserts every cited
filename exists, and warns on remaining `TODO` markers.

## Troubleshooting

- **Port collisions**: copy `.env.example` to `.env` and override the
  `LAB_*_PORT` values that conflict.
- **`make ps` stuck in `health: starting`**: RabbitMQ and Redpanda take
  ~20s to report healthy; the producer/consumers wait for them.
- **`make seed` PRECONDITION_FAILED on a queue**: a queue already exists
  with different arguments. `make clean` (drops volumes) then `make up`
  and re-seed; the topology arguments must match `LAB_QUEUE_MAX_LEN`.
- **No data in Grafana**: let a bench run for ~2 minutes — the dashboard
  uses recording rules with a 1m window.
- **Switching the active broker**: set `LAB_BROKER=redpanda` in `.env`
  to make the producer publish to Redpanda instead of RabbitMQ (the
  consumer fleet always reads RabbitMQ; Redpanda is for comparison).
