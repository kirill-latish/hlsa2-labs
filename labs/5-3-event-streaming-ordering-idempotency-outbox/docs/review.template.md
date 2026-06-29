# Architecture review - lab 5-3 (event streaming: ordering, idempotency, outbox, replay)

> Copy to `docs/review.md` and fill the TODOs.
> Target: ~1,500-2,000 words. Every quantitative claim must cite a file
> under `perf/results/` or `docs/img/`. `make check-submission` enforces
> that every cited filename exists.
>
> For each experiment answer the four questions: **what did you measure**
> (cite the artifact), **what changed**, **why** in terms of the
> mechanism, and **what new risk** the change introduces.

---

## 1. Environment & method

- **Fingerprint**: `perf/results/env.txt` (and `perf/results/meta.json`)
- **Broker / partitions / consumers / postgres / RTTs**: TODO (from `perf/results/meta.json`)
- **Workload model**: `perf/workload.json`
- **Lab version**: TODO (git rev) - see `perf/results/env.txt`

Method: labelled runs under `make bench-baseline`, `make bench-ordering`,
the idempotency `verify-exactly-once` proofs, the consistency
`analyze-consistency` proofs, and the `replay-rebuild` recovery. Each
writes a JSON/Markdown artifact under `perf/results/`.

## 2. Baseline pipeline and the duplicate rate

Citation: `perf/results/baseline/report.md` (runs in
`perf/results/baseline/run1/`, `run2/`, `run3/`).

| metric                     | median | sigma |
| -------------------------- | -----: | ----: |
| throughput (events/s)      | TODO   | TODO  |
| consumer lag count (peak)  | TODO   | TODO  |
| consumer lag time (s, peak)| TODO   | TODO  |
| duplicate rate             | TODO   | TODO  |

- **Measured**: TODO. **Why** the duplicate rate is low-but-nonzero
  (normal at-least-once), not a bug. **Risk**: a flat zero would mean
  dedup is never exercised or events are lost.
- Dashboard screenshot: `docs/img/baseline-dashboard.png`

## 3. Idempotency proven under duplicates + crash

Citations: `perf/results/idempotency-naive/result.json`,
`perf/results/idempotency-after/result.json`,
`perf/results/idempotency-after/compare-vs-before.md`.

| metric              | naive | idempotent |
| ------------------- | ----: | ---------: |
| side_effects_total  | TODO  | TODO       |
| unique_events       | TODO  | TODO       |
| side_effect_ratio   | TODO  | TODO       |
| exactly_once_effect | false | true       |

- **What changed / why**: TODO - at-least-once + idempotency =
  exactly-once *effect* (not delivery). The dedup marker + the effect
  commit in one transaction, so the injected duplicates AND the
  replay-on-restart are both suppressed.
- **Residual risk**: TODO - the dedup window must exceed the maximum
  redelivery delay, or a late redelivery slips past dedup.

## 4. Ordering violations under wrong vs correct partition key

Citations: `perf/results/ordering-wrong/summary.json`,
`perf/results/ordering-entity/summary.json`,
`perf/results/ordering-entity/compare-vs-before.md`.

| metric                  | wrong key | entity key |
| ----------------------- | --------: | ---------: |
| ordering_violations     | TODO      | TODO       |
| ordering_violation_rate | TODO      | TODO       |

- **What changed / why**: TODO - ordering holds only within a partition;
  per-entity key (`order_id`) is the only ordering guarantee that
  matters, and global ordering is unavailable at scale.
- **Residual risk**: TODO - a per-entity key can create a hot partition
  (celebrity entity), trading ordering for load skew.
- Dashboard screenshot: `docs/img/ordering-violations.png`

## 5. Dual-write inconsistency and the outbox fix

Citations: `perf/results/dualwrite-naive/result.json`,
`perf/results/dualwrite-outbox/result.json`,
`perf/results/dualwrite-outbox/compare-vs-before.md`.

| metric                 | naive-dualwrite | outbox |
| ---------------------- | --------------: | -----: |
| orders_written         | TODO            | TODO   |
| projection_orders      | TODO            | TODO   |
| orphaned_state_changes | TODO            | 0      |

- **What changed / why**: TODO - the outbox converts a distributed
  dual-write into a local atomic write (business row + outbox row in one
  tx) plus an asynchronous, order-preserving relay.
- **Residual risk**: TODO - outbox delivery is still at-least-once
  (consumers stay idempotent), the outbox table needs lifecycle/cleanup,
  and the relay must preserve commit order.

## 6. Replay-to-rebuild and its recovery-time implication

Citations: `perf/results/replay/replay.json`,
`perf/results/replay/analysis.json`,
`perf/results/replay/no-side-effects.json`.

- **Rebuild duration (recovery time)**: TODO s (from `perf/results/replay/replay.json`)
- **Projection rebuilt correctly**: TODO (from `perf/results/replay/analysis.json`)
- **No side effects re-fired**: TODO (from `perf/results/replay/no-side-effects.json`)
- **Why**: TODO - retention bounds what you can replay; offsets must be
  committed *after* durable processing, not before; rebuild-only suppresses
  external effects while reprocess re-fires them.

## 7. Decision-ladder justification + residual risks

Use the topic's decision ladder to justify where this workload belongs:

> no events -> simple queue -> event stream with idempotent consumers ->
> outbox-backed stream -> full event sourcing

- This workload belongs at: TODO (justify with the evidence above).
- Why not one rung lower / higher: TODO.

Residual risks for a production runbook:

- Dedup window vs maximum redelivery delay (idempotency).
- Hot-partition skew from a per-entity key (ordering).
- Outbox table growth / cleanup and relay commit-order preservation.
- Replay retention bounds and the rebuild-only vs reprocess discipline.

Runbooks: `runbooks/duplicate-storm-incident.md`,
`runbooks/dual-write-inconsistency-incident.md`.

---

## Reproducibility note

```
cp .env.example .env
make up && make seed && make env-fingerprint
make bench-baseline RUNS=3 DURATION=5m && make analyze-baseline
make apply-fix CANDIDATE=naive-consumer && make inject-duplicates RATE=20pct && make crash-consumer-midbatch && make verify-exactly-once LABEL=idempotency-naive
make apply-fix CANDIDATE=idempotent-consumer && make inject-duplicates RATE=20pct && make crash-consumer-midbatch && make verify-exactly-once LABEL=idempotency-after
make compare-idempotency BEFORE=idempotency-naive AFTER=idempotency-after
make bench-ordering KEY=wrong DURATION=3m LABEL=ordering-wrong
make bench-ordering KEY=entity DURATION=3m LABEL=ordering-entity
make compare-ordering BEFORE=ordering-wrong AFTER=ordering-entity
make apply-fix CANDIDATE=naive-dualwrite && make inject-crash-between-writes LABEL=dualwrite-naive && make analyze-consistency LABEL=dualwrite-naive
make apply-fix CANDIDATE=outbox && make inject-crash-between-writes LABEL=dualwrite-outbox && make analyze-consistency LABEL=dualwrite-outbox
make compare-consistency BEFORE=dualwrite-naive AFTER=dualwrite-outbox
make corrupt-projection && make replay-rebuild FROM=earliest MODE=rebuild-only LABEL=replay && make analyze-replay LABEL=replay && make verify-no-side-effects LABEL=replay
make check-submission
```
