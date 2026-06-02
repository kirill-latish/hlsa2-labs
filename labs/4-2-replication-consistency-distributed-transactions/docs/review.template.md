# Lab 4-2 Review — Replication, Consistency, and Distributed Transactions

> Replace every `<...>` and `TODO` with your own number / claim.
> Every quantitative claim **must cite a file** under `perf/results/` or `docs/img/`.
> Target length: 1500–2000 words.

## 1. Environment & method

- Host: `<CPU model + cores>`, `<RAM>` GB, `<OS>`. See `perf/results/env.txt`.
- Postgres versions: see `perf/results/env.txt`.
- Replica count: 2 streaming, async (`hot_standby=on`).
- Inter-container RTT (median): `<ms>` (see `perf/results/env.txt`).
- Per-experiment workload settings (RATE, DURATION, RUNS) listed in each
  section's commands. Knobs default to the values committed in
  `perf/workload.json`.

Each numbered section below answers four questions: **what did you
measure**, **what changed**, **why** (in terms of the mechanism), and
**what new risk** the change introduces.

## 2. Replication lag distribution

- Command: `make bench-lag RUNS=3 DURATION=5m`, then `make analyze-lag`.
- Artifacts: `perf/results/lag/run1/samples.csv`, `perf/results/lag/run2/samples.csv`, `perf/results/lag/run3/samples.csv`.

| run | replica | p50 | p95 | p99 | p99.9 | max |
|----|---------|-----|-----|-----|-------|-----|
| run1 | replica-1 | TODO | TODO | TODO | TODO | TODO |
| run1 | replica-2 | TODO | TODO | TODO | TODO | TODO |
| run2 | replica-1 | TODO | TODO | TODO | TODO | TODO |
| run3 | replica-1 | TODO | TODO | TODO | TODO | TODO |

- Run-to-run sigma at p99: `<...>` ms (see `perf/results/lag/summary.json`).
- **Implication for read policy**: `<your interpretation>` — for example,
  "p99.9 hit `<X>`ms under sustained 500 writes/s, so any policy that
  routes `now-T_pin < X` reads to a replica is unsafe."
- Screenshot: `docs/img/lag-overview.png`.

## 3. Read-after-write violation rate before vs after one fix

- Commands: `make bench-raw MODE=naive`, `make bench-raw MODE=session-pin`,
  `make compare-raw BEFORE=naive AFTER=session-pin`.
- Artifacts: `perf/results/raw/naive/summary.json`,
  `perf/results/raw/session-pin/summary.json`.

| metric | naive | session-pin |
|--------|-------|-------------|
| reads | TODO | TODO |
| violations | TODO | 0 (or near-zero) |
| violation rate | TODO | TODO |
| read p99 (ms) | TODO | TODO |

- **Why session-pin works**: a session that just wrote a row reads the
  same row from the primary for `T_pin` ms, so the write is visible
  before the replica's apply lag has had time to expose a stale row.
- **Residual trade-off**: hot sessions lose replica scaling for the
  pinned window. Quantify it from the throughput row of `compare-raw`.

## 4. 2PC vs saga under fault — success rate, p99, lock-hold

- Commands:
  - `make bench-2pc RUNS=3 LABEL=healthy`
  - `make bench-saga RUNS=3 LABEL=healthy`
  - `make inject-fault SERVICE=inventory MODE=latency P99_MS=2000`
  - `make bench-2pc RUNS=3 LABEL=faulted`
  - `make bench-saga RUNS=3 LABEL=faulted`
  - `make compare-2pc-saga`
  - `make clear-fault SERVICE=inventory`
- Artifacts:
  - `perf/results/2pc/healthy/summary.json`
  - `perf/results/2pc/faulted/summary.json`
  - `perf/results/saga/healthy/summary.json`
  - `perf/results/saga/faulted/summary.json`

| metric | 2pc.healthy | 2pc.faulted | saga.healthy | saga.faulted |
|--------|-------------|-------------|---------------|---------------|
| Requests | TODO | TODO | TODO | TODO |
| Success rate (median) | TODO | TODO | TODO | TODO |
| p99 latency (ms) | TODO | TODO | TODO | TODO |
| Compensations | n/a | n/a | TODO | TODO |
| 2PC in-doubt count peak | TODO | TODO | n/a | n/a |

- **Why saga held under fault**: each step commits locally + outbox in
  one transaction; the inventory latency stretches step 2 but doesn't
  block the others. Failure becomes a business-level compensation
  rather than a stuck transaction.
- **Why 2PC collapsed under fault**: prepare phase holds locks until
  commit; the 2s injected latency widens the in-doubt window and other
  transactions queue behind those locks. See `pg_locks` snapshot
  reference in the dashboard screenshot `docs/img/twopc-in-doubt.png`.
- **Trade-off the saga introduces**: intermediate-state visibility
  (between step 2 and step 3 the order is "paid + reserved + not
  shipped"). Mitigation: the saga's final-state guarantee + idempotent
  consumers (section 5).

## 5. Saga idempotency proof + fault-injected compensation

- Commands:
  - `make seed-events WINDOW=24h`
  - `make replay WINDOW=24h CONSUMER_MODE=idempotent`
  - `make assert-idempotent CONSUMER_MODE=idempotent`
  - `make assert-idempotent CONSUMER_MODE=naive`
  - `make inject-fault SERVICE=shipping MODE=fail`
  - `make bench-saga RUNS=1 LABEL=compensation-test`
  - `make clear-fault SERVICE=shipping`
- Artifacts:
  - `perf/results/replay/idempotent/summary.json`
  - `perf/results/replay/naive/summary.json`
  - `perf/results/saga/compensation-test/summary.json`

| mode | hash replay #1 | hash replay #2 | match |
|------|----------------|----------------|-------|
| idempotent | TODO | TODO | ✓ |
| naive | TODO | TODO | ✗ |

- Saga compensation test: `perf/results/saga/compensation-test/run1/summary.json`
  shows `compensated_count=<N>` matching `failed=<N>` with no
  double-firing — the dedupe key in `processed_events` keeps
  retried compensations idempotent.

- **Why idempotent mode survives replay**: the consumer's first action
  per event is `INSERT INTO processed_events(event_id) ON CONFLICT
  DO NOTHING`. If the row was already there, the side-effect is
  skipped. Both writes commit in the same transaction so a crash
  mid-handler can't leave them inconsistent.

## 6. Targeted improvement + 2-sigma decision

- Candidate chosen: `<lsn-wait-on-raw|outbox-cdc|replace-2pc-with-saga>`.
- Commands: `make regression CANDIDATE=<...> RUNS=3`, `make analyze CANDIDATE=<...>`.
- Artifacts: `perf/results/regression/<candidate>/summary.json` plus
  per-run summaries under `baseline/runN` and `candidate/runN`.

| side | metric median | sigma | n |
|------|---------------|-------|---|
| baseline | TODO | TODO | 3 |
| candidate | TODO | TODO | 3 |

- Delta: `<...>` (`>` or `<` `2 * max(sigma) = <...>`?).
- **Decision**: `<REAL change | within noise>`.
- **New risk introduced**: `<for example, lsn-wait adds a coordination
  call per read; CDC adds operational complexity; saga adds
  intermediate-state visibility>`.

## 7. Closing — per-operation consistency policy + residual risks

Apply the topic's per-operation consistency decision tree
(monotonic-reads vs read-after-write vs strong) to your three actual
operations and justify the policy you'd ship to production:

- **read-account-balance**: `<chosen consistency level + why>`.
- **place-order**: `<chosen consistency level + why; saga vs 2PC>`.
- **read-recent-orders**: `<chosen consistency level + why>`.

**Residual risks for the runbook**:

1. `<for example, an outage of the LSN-wait coordination raises
    end-to-end p99>`.
2. `<for example, replay storms can re-fire compensations if dedupe
    keys collide>`.
3. `<...>`.

Linked runbooks: `runbooks/replica-lag-incident.md`,
`runbooks/saga-stuck-incident.md`.
