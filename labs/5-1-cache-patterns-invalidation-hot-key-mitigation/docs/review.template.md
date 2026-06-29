# Architecture review - lab 5-1 (cache patterns, invalidation, hot-key mitigation)

> Copy to `docs/review.md` and fill the TODOs.
> Target: ~1,500-2,000 words; every quantitative claim must cite a file
> under `perf/results/` or `docs/img/`. `make check-submission` enforces
> this. For each experiment answer the four questions: **what did you
> measure** (cite the artifact), **what changed**, **why** (the
> mechanism), and **what new risk** the change introduces.

---

## 1. Environment & method

- **Captured**: `perf/results/env.txt` and `perf/results/meta.json`
- **Workload model**: `perf/workload.json` (Zipfian alpha~1.0 baseline,
  stampede at TODO RPS, hot-key weight TODO, staleness writers TODO)
- **Topology**: 3 client-side-sharded Redis nodes, 1 app, 1 Postgres SoR
- **Lab version**: TODO (git rev) - see `perf/results/env.txt`

Method: counters are diffed per run from `app-metrics-before.txt` and
`app-metrics.txt`; baseline uses 3 runs with run-to-run sigma. Note the
client-side-sharding decision (see README) and why it preserves the
per-shard imbalance signal.

## 2. Baseline under Zipfian load

Citation: `perf/results/baseline/report.md` (runs in
`perf/results/baseline/run1/`, `run2/`, `run3/`).

| metric            | median | sigma |
| ----------------- | -----: | ----: |
| hit_ratio         | TODO % | TODO  |
| source_fetch_rate | TODO   | TODO  |
| read p50 (ms)     | TODO   | TODO  |
| read p99 (ms)     | TODO   | TODO  |

- What hit ratio + latency + source-fetch rate together tell you: TODO
- Why uniform load would have been misleading here: TODO
- Baseline dashboard screenshot: `docs/img/baseline.png`

## 3. Cache stampede reproduction

Setup: `make inject-stampede TTL=30s HOT_RATE=5000 DURATION=3m
LABEL=stampede-baseline` (coalescing `none`, jitter 0).

Citation: `perf/results/stampede-baseline/fan_in_ratio.json`,
`perf/results/stampede-baseline/report.md`.

- fan_in_ratio at expiration: TODO (expect hundreds)
- What changed at t=TTL (source fetches, DB pressure, hot-key p99): TODO
- Why (synchronized expiry + the SoR fetch window): TODO
- Screenshot of the spike: `docs/img/stampede-spike.png`

## 4. TTL jitter + coalescing fix

Setup: `make apply-fix CANDIDATE=jitter JITTER=20pct` then
`make apply-fix CANDIDATE=<singleflight|xfetch|swr>`, re-run the
identical injection as `LABEL=stampede-after`.

Citation: `perf/results/stampede-after/fan_in_ratio.json`,
`perf/results/stampede-after/compare-vs-before.md`.

| metric        | before | after | factor |
| ------------- | -----: | ----: | -----: |
| fan_in_ratio  | TODO   | TODO  | TODO   |
| read p99 (ms) | TODO   | TODO  | TODO   |

- Why jitter alone does not fix a *single* hot key (one key, one expiry): TODO
- Mechanism of your chosen coalescing mode: TODO
- Residual risk (singleflight = shared failure; xfetch = probability
  tuning; SWR = serves slightly stale): TODO

## 5. Hot-key per-shard imbalance + local LRU

Setup: `make inject-hot-key KEY=celebrity-1 WEIGHT=0.4`,
`make bench-hot-key LABEL=hot-baseline`, then
`make apply-fix CANDIDATE=local-lru LOCAL_SIZE=1000 LOCAL_TTL=5s`,
`make bench-hot-key LABEL=hot-after`.

Citation: `perf/results/hot-baseline/per-shard.json`,
`perf/results/hot-after/per-shard.json`,
`perf/results/hot-after/compare-hot-key.md`.

- Per-shard share before (one shard ~50%, others ~20%): TODO
- Per-shard share after local LRU (rebalanced): TODO
- Why cluster-average ops/sec hid the imbalance: TODO
- Residual trade-off (per-process inconsistency for the local TTL
  window): TODO
- Screenshot of per-node ops/sec: `docs/img/hot-key-shards.png`

## 6. Invalidation staleness rate

Setup: `make bench-staleness STRATEGY=ttl-only TTL=60s` and
`make bench-staleness STRATEGY=explicit-invalidate`.

Citation: `perf/results/staleness/report.md`,
`perf/results/staleness/ttl-only/summary.json`,
`perf/results/staleness/explicit/summary.json`.

| strategy            | fraction_stale | max_staleness_s |
| ------------------- | -------------: | --------------: |
| ttl-only            | TODO %         | TODO            |
| explicit-invalidate | TODO %         | TODO            |

- Why TTL-only staleness is bounded by the TTL: TODO
- Why explicit invalidation approaches zero, and what happens if one
  writer path skips the invalidate: TODO
- Chosen policy is documented in `docs/staleness-policy.md`.

## 7. Decision ladder + residual risks (production runbook)

Cache decision ladder used to justify the patterns applied:

- Skewed read traffic, tolerable freshness -> **cache-aside + TTL**
- Synchronized expiry on a hot key -> **TTL jitter + coalescing**
  (singleflight / xfetch / SWR)
- One key hotter than a whole shard -> **local in-process LRU**
- Writes must be visible quickly -> **explicit invalidation** (accept
  the coverage burden) instead of TTL-only

Residual risks the lab does NOT cover: multi-instance local-cache
coherence, Redis node failure/failover, cache penetration (missing
keys), negative caching, and write-through vs write-behind durability.

Runbooks: `runbooks/cache-stampede-incident.md`,
`runbooks/hot-key-incident.md`.

---

## Reproducibility note

```
cp .env.example .env
make up && make seed && make env-fingerprint
make bench-baseline DIST=zipf RUNS=3 DURATION=5m && make analyze-baseline
make inject-stampede TTL=30s HOT_RATE=5000 DURATION=3m LABEL=stampede-baseline
make analyze-stampede LABEL=stampede-baseline && make clear-stampede
make apply-fix CANDIDATE=jitter JITTER=20pct
make apply-fix CANDIDATE=singleflight
make inject-stampede TTL=30s HOT_RATE=5000 DURATION=3m LABEL=stampede-after
make analyze-stampede LABEL=stampede-after
make compare-stampede BEFORE=stampede-baseline AFTER=stampede-after && make clear-stampede
make inject-hot-key KEY=celebrity-1 WEIGHT=0.4
make bench-hot-key DURATION=3m LABEL=hot-baseline
make apply-fix CANDIDATE=local-lru LOCAL_SIZE=1000 LOCAL_TTL=5s
make bench-hot-key DURATION=3m LABEL=hot-after
make compare-hot-key && make clear-hot-key
make bench-staleness STRATEGY=ttl-only TTL=60s DURATION=5m
make bench-staleness STRATEGY=explicit-invalidate DURATION=5m
make analyze-staleness
make check-submission
```
