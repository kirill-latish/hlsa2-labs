# Lab 4-3 — NoSQL, Sharding, Polyglot Persistence, and CDC: Architecture Review

> Target length: 1500–2000 words. Every quantitative claim must cite a specific
> file under `perf/results/` (or an image under `docs/img/`). Statements like
> "the fix is better" are worth zero; "the composite key cut max/mean partition
> skew from 8.3× to 1.4× and lifted throughput from 12K to 47K writes/s under
> the identical celebrity workload, see
> `perf/results/skew/shard-key-after/run-2/partition_metrics.json` and the
> 2-sigma decision in `perf/results/compare-skew.txt`" earn full marks.

## 1. Environment & method

- Host: <CPU / cores / RAM / disk class — see `perf/results/env.txt`>
- Container fingerprint: `perf/results/meta.json`
- Stack: Postgres 16 (system of record, logical replication via `pgoutput`),
  MongoDB 7 sharded cluster (3 single-mongod shards + 3 config-server replica
  set + 2 mongos routers), Debezium Connect 2.7, Redpanda 24.2, Elasticsearch
  8.15, Go 1.22 services. See `docs/architecture.md`.
- Method: each experiment was run from a clean `make up && make seed` and
  follows the same warmup → measurement → snapshot pattern. Sigma figures
  come from at least three independent runs.

## 2. Per-partition skew under the candidate shard key

- What I measured: per-shard insert rate, CPU user-seconds, doc count
  for the four pre-sharded collections in `lab43`. Driven by
  `make bench-skew SHARD_KEY=candidate RUNS=3 DURATION=5m`.
- Artifact: `perf/results/skew/shard-key-baseline/run-{1,2,3}/partition_metrics.json`
- Result: <max/mean and per-shard share>; <how cluster averages hid the
  imbalance>.
- Why: shard key cardinality, hot tenant skew, chunk bounds.
- Risk: <what this hides if you only watch the cluster aggregate>.

## 3. Hot-partition reproduction and the ladder fix

- Reproduction: `make inject-hot ENTITY=tenant-A WEIGHT=0.35 && make bench-skew
  RUNS=3 LABEL=hot-injected`. Artifacts under
  `perf/results/skew/hot-injected/`.
- Ladder fix attempted: <hash-suffix | composite-key | resharded — pick one
  and justify>.
- Identical workload re-run: `make apply-fix CANDIDATE=<choice> &&
  make bench-skew SHARD_KEY=fixed LABEL=shard-key-after RUNS=3` while
  the hot entity is still injected.
- Side-by-side: `perf/results/skew/compare.txt` (output of `make compare-skew
  BEFORE=hot-injected AFTER=shard-key-after`).
- Decision: <APPLY/REGRESSION/NEUTRAL by the 2-sigma rule>.
- Risk: <reshard cost, query plan changes, mongos targeted-vs-broadcast>.

## 4. CDC lag distribution at base and 2× load

- Driven by `make bench-cdc-lag RUNS=3 RATE=1x LABEL=base` and
  `make bench-cdc-lag RUNS=3 RATE=2x LABEL=2x`.
- Artifacts: `perf/results/cdc-lag/base/run-*/lag_samples.csv`,
  `perf/results/cdc-lag/2x/run-*/lag_samples.csv`,
  `perf/results/cdc-lag/analyze.txt`.
- Result: p50/p95/p99/p99.9 at base vs 2×.
- Why: WAL throughput, Debezium task batching, Redpanda fetch size,
  ES bulk indexer settings.
- Risk: lag tail explosion, replication slot growth, retention.

## 5. Polyglot read path with explicit freshness policy

- Bench: `make bench-polyglot RUNS=3 FRESHNESS=<read-from-sor|read-from-derived|lsn-wait>`.
- Artifacts: `perf/results/polyglot/<policy>/run-*/summary.json` +
  `polyglot_samples.csv`.
- Result: violations %, fallback-to-SoR rate, average wait when using `lsn-wait`.
- Decision: which policy is correct for which API surface — and why
  the boundary is data-criticality, not technology.
- Risk: stale-read amplification under CDC outage, lsn-wait blocking.

## 6. Decision tree applied to my system + residual risks

Apply the topic's decision tree to one or two real surfaces of your stack:

- **Right store for the workload** — what data needs SoR semantics, what can
  live in a derived store, what is purely cache.
- **Shard-key cost** — what's a healthy starting cardinality and what's a
  signal it's wrong (max/mean > 1.5? top-1 > 35% of writes?).
- **CDC adoption threshold** — when does it pay for itself vs dual-writes,
  and what observability you need before going live.

Close with three residual risks for a production runbook.

---

## Required artifacts

- `perf/results/env.txt`, `perf/results/meta.json`
- `perf/results/skew/{shard-key-baseline,hot-injected,shard-key-after}/run-*/partition_metrics.json`
- `perf/results/skew/compare.txt`
- `perf/results/cdc-lag/{base,2x}/run-*/{lag_samples.csv,summary.json}`
- `perf/results/cdc-lag/analyze.txt`
- `perf/results/polyglot/<policy>/run-*/summary.json`
- `docs/freshness-policy.md` (copied from `docs/freshness-policy.template.md`)
- `runbooks/hot-partition-incident.md`, `runbooks/cdc-stuck-incident.md`
