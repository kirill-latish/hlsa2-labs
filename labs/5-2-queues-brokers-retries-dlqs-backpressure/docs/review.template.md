# Architecture review - lab 5-2 (queues, brokers, retries, DLQs, backpressure)

> Copy to `docs/review.md` and fill the TODOs.
> Target: ~1,500-2,000 words; every quantitative claim must cite a file
> under `perf/results/` or `docs/img/`. `make check-submission` enforces
> that every cited filename exists.
>
> For each experiment answer the four questions: **what did you
> measure** (cite the artifact), **what changed**, **why** (the
> mechanism), and **what new risk** the change introduces.

---

## 1. Environment & method

- **Captured**: `perf/results/env.txt` and `perf/results/meta.json`
- **Broker versions**: RabbitMQ TODO, Redpanda TODO (see `perf/results/meta.json`)
- **Consumer instances**: TODO (3)
- **Workload model**: `perf/workload.json` (baseline at TODO msg/s for TODO; overload at 2x)
- **Lab version**: TODO (git rev) - see `perf/results/env.txt`

Method: each labelled condition is driven via `make` targets that
snapshot `summary.json` + `metrics.txt` (+ a `metrics-before.txt` for
per-run counter deltas) under `perf/results/<label>/runN/`.

## 2. Baseline pipeline (lag count AND time, not just throughput)

Citation: `perf/results/baseline/report.md`

| metric                     | median | sigma |
| -------------------------- | -----: | ----: |
| throughput (msg/s)         | TODO   | TODO  |
| consumer lag - count       | TODO   | TODO  |
| consumer lag - time (s)    | TODO   | TODO  |
| processing p50 (ms)        | TODO   | TODO  |
| processing p99 (ms)        | TODO   | TODO  |
| DLQ rate (msg/s)           | TODO   | TODO  |

- Lag is bounded and stable (we are below capacity) - confirms the
  baseline is healthy. **Why report lag and not just throughput?** TODO
  (one sentence: a consumer that throughputs 10K/s on a 12K/s workload
  looks fine on a throughput chart but has lag growing at 2K/s).
- Baseline dashboard screenshot: `docs/img/baseline.png`

## 3. Poison-message collapse and the bounded-retry + DLQ fix

Setup: `make inject-poison COUNT=1 LABEL=poison-baseline DURATION=3m`
(unbounded retries), then
`make apply-fix CANDIDATE=bounded-retry MAX_RETRIES=5` and re-inject as
`poison-after`.

Citation: `perf/results/poison-baseline/report.md`,
`perf/results/poison-after/report.md`,
`perf/results/poison-after/compare-vs-before.md`.

| metric                  | before | after | delta |
| ----------------------- | -----: | ----: | ----: |
| cluster throughput      | TODO   | TODO  | TODO  |
| retry rate (retries/s)  | TODO   | TODO  | TODO  |
| DLQ count               | TODO   | TODO  | TODO  |
| lag (count)             | TODO   | TODO  | TODO  |

- **What changed / why**: bounded retries with exponential backoff +
  jitter break the poison loop; after MAX_RETRIES the message
  dead-letters and the consumer slot frees. TODO
- **Residual trade-off**: TODO (small MAX_RETRIES may DLQ recoverable
  messages prematurely; large N wastes capacity on broken messages).
- Collapse screenshot: `docs/img/poison-collapse.png`

## 4. Retry/DLQ classification under fault injection

Setup: `make apply-fix CANDIDATE=classify-failures` then
`make bench-faults TRANSIENT_RATE=10pct PERMANENT_RATE=2pct DURATION=5m LABEL=faults`.

Citation: `perf/results/faults/report.md`.

- Retry rate (non-zero, tracks transient rate): TODO
- DLQ rate (non-zero, tracks permanent rate): TODO
- p99 under fault injection (elevated but bounded): TODO
- **Why permanent failures should skip retries**: TODO (retry will never
  succeed; it wastes consumer capacity and delays DLQ visibility).

## 5. Backpressure benchmark and lag-over-time interpretation

Setup: `make apply-fix CANDIDATE=backpressure-signal` then
`make bench-backpressure RATE=2x DURATION=10m LABEL=backpressure`.

Citation: `perf/results/backpressure/report.md`,
`docs/img/backpressure-lag.png`.

- Lag-over-time outcome: TODO (stabilized = backpressure honored /
  unbounded growth = ignored).
- Producer error rate: TODO (non-zero if backpressure propagates; zero
  if the producer is unaware).
- **Design implication**: TODO (if lag stabilized, the system degrades
  gracefully; if it grew unboundedly, the producer must be fixed to
  honor backpressure signals).

## 6. Decision ladder + residual risks

Use the topic's decision ladder to justify the messaging choices:

> no queue -> in-process -> managed -> self-managed queue-based ->
> self-managed log-based -> multi-broker

- Why a broker at all here (vs in-process)? TODO
- RabbitMQ (queue-based) vs Kafka/Redpanda (log-based) for this
  workload? TODO (per-message retry/DLQ semantics vs replayable log;
  cite which you would ship and why).
- Residual risks for the production runbook: TODO (DLQ drain policy,
  retry-budget interaction, broker HA, exactly-once vs at-least-once,
  consumer scaling).

Runbooks: `runbooks/poison-message-incident.md`,
`runbooks/backpressure-incident.md`.

---

## Reproducibility note

```
cp .env.example .env
make up && make seed && make env-fingerprint
make brokers-status && make consumer-status
make bench-baseline RUNS=3 DURATION=5m && make analyze-baseline
make inject-poison COUNT=1 LABEL=poison-baseline DURATION=3m
make analyze-poison LABEL=poison-baseline
make apply-fix CANDIDATE=bounded-retry MAX_RETRIES=5
make inject-poison COUNT=1 LABEL=poison-after DURATION=3m
make analyze-poison LABEL=poison-after
make compare-poison BEFORE=poison-baseline AFTER=poison-after
make clear-poison
make apply-fix CANDIDATE=classify-failures
make bench-faults TRANSIENT_RATE=10pct PERMANENT_RATE=2pct DURATION=5m LABEL=faults
make analyze-faults LABEL=faults
make apply-fix CANDIDATE=backpressure-signal
make bench-backpressure RATE=2x DURATION=10m LABEL=backpressure
make analyze-backpressure LABEL=backpressure
make check-submission
```
