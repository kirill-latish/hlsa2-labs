# HLSA2 Lab 5-1 — Cache Patterns, Invalidation, and Hot-Key Mitigation

Companion lab for topic 250. You will:

1. Stand up a cached web application: a Go **app** (cache-aside / write-
   through with runtime-flippable knobs), a **3-node sharded Redis**
   cache, a **Postgres** system of record with simulated query latency,
   a Go **loadgen** with Zipfian/uniform/hot-key/staleness modes, and a
   pre-provisioned **Prometheus + Grafana** pair with a `Cache Overview`
   dashboard.
2. Measure a baseline under realistic **Zipfian** load (3 runs, sigma).
3. Reproduce a **cache stampede** and watch the database melt under the
   fan-in at TTL expiry.
4. Apply **TTL jitter + a coalescing fix** (singleflight / xfetch / SWR)
   and drive the fan-in ratio back to ~1.
5. Inject a **hot key** on the sharded cache, prove per-shard imbalance
   is invisible in cluster averages, then apply **local LRU**.
6. Measure **invalidation staleness** under a writer/reader race.
7. Write the architecture review, the staleness policy, and runbooks.

> The cache-pattern toggles default to the unmitigated state (no jitter,
> no coalescing, no local LRU, TTL-only invalidation) so the stampede in
> step 3 and the hot-key imbalance in step 5 are real. Apply fixes with
> `make apply-fix`; `.env` is for host port overrides only.

## Design decision: client-side sharding (not Redis Cluster)

The topic guide talks about a "Redis Cluster with 3 shards" so per-node
imbalance is visible. **This lab shards CLIENT-SIDE instead**: the app
hashes each key with CRC32 and maps it to one of three *standalone*
Redis nodes (`hash % 3`, see [`internal/shard/`](internal/shard/)).

Why deviate?

- **Same teaching property.** A single hot key still deterministically
  lands on exactly one node, so the per-node ops/sec panel shows the
  imbalance the guide wants — and `make cluster-status` prints per-node
  key counts.
- **Far more robust in Docker.** No cluster bus, no gossip, no slot
  migration, no `MOVED` redirects, no 16384-slot bootstrap that flakes
  on a laptop. The stack comes up deterministically every time.

The trade-off (no automatic resharding/failover) is irrelevant to what
this lab measures and is called out here so the simplification is honest.

## Prerequisites

- Docker + Docker Compose (Docker Desktop or equivalent).
- `make`, `bash`, `jq`, `python3` (3.10+), `curl` on the host.
- ~3 GB of free disk for images + Postgres + Prometheus TSDB.
- Ports `3000`, `8080`, `8090`, `9090`, `15432`, `16391-16393` free on
  the host (override via `.env` from `.env.example` if any clash).

## Stack overview

| Service      | Port  | Role                                                  |
| ------------ | ----- | ---------------------------------------------------- |
| `app`        | 8080  | Cache-aside app; `/read`, `/write`, `/source`, `/admin/config` |
| `redis-1..3` | 16391-16393 | Standalone cache nodes (client-side sharded)   |
| `postgres`   | 15432 | System of record (simulated query latency)           |
| `loadgen`    | 8090  | In-cluster Go load driver + staleness probe          |
| `prometheus` | 9090  | Scrapes app + loadgen every 5s                       |
| `grafana`    | 3000  | Provisioned **Cache Overview** dashboard             |

All Go services share one `go.mod` at the lab root and helpers under
[`internal/`](internal/) (metrics, shard ring, local LRU).

## One-line bring-up

```bash
cp .env.example .env
make up
make seed
make env-fingerprint
make cluster-status
```

You should see all containers healthy, `cache_items` populated, the
warm set spread across the three shards, and `CLUSTER_OK`.

## Runtime cache knobs (POST /admin/config)

The app reads its behaviour from a snapshot taken at the top of every
request, so `make apply-fix` flips knobs between runs with no restart.

| Knob                    | Values                                   | Set by |
| ----------------------- | ---------------------------------------- | ------ |
| `cache_ttl_seconds`     | base TTL written on a miss               | inject-stampede / bench-staleness |
| `ttl_jitter_pct`        | spread expiry (e.g. 20)                  | `apply-fix CANDIDATE=jitter` |
| `coalescing`            | `none`/`singleflight`/`xfetch`/`swr`     | `apply-fix CANDIDATE=<mode>` |
| `local_lru` / size / ttl| in-process LRU short-circuiting hot keys | `apply-fix CANDIDATE=local-lru` |
| `invalidation`          | `ttl-only`/`explicit-invalidate`         | `bench-staleness STRATEGY=...` |

Read the active config any time: `curl http://localhost:8080/admin/config`.

## Steps from the topic guide

| Step | Command(s) |
| ---- | ---------- |
| 1 | `make up`, `make seed`, `make ps`, `make cluster-status` |
| 2 | `make env-fingerprint` |
| 3 | `make bench-baseline DIST=zipf RUNS=3 DURATION=5m`, `make analyze-baseline` |
| 4 | `make inject-stampede TTL=30s HOT_RATE=5000 DURATION=3m LABEL=stampede-baseline`, `make analyze-stampede LABEL=stampede-baseline`, `make clear-stampede` |
| 5 | `make apply-fix CANDIDATE=jitter JITTER=20pct`, `make apply-fix CANDIDATE=singleflight`, `make inject-stampede ... LABEL=stampede-after`, `make analyze-stampede LABEL=stampede-after`, `make compare-stampede BEFORE=stampede-baseline AFTER=stampede-after`, `make clear-stampede` |
| 6 | `make inject-hot-key KEY=celebrity-1 WEIGHT=0.4`, `make bench-hot-key DURATION=3m LABEL=hot-baseline`, `make apply-fix CANDIDATE=local-lru LOCAL_SIZE=1000 LOCAL_TTL=5s`, `make bench-hot-key DURATION=3m LABEL=hot-after`, `make compare-hot-key`, `make clear-hot-key` |
| 7 | `make bench-staleness STRATEGY=ttl-only TTL=60s DURATION=5m`, `make bench-staleness STRATEGY=explicit-invalidate DURATION=5m`, `make analyze-staleness` |
| 8 | Fill in `docs/review.md` from `docs/review.template.md` |
| 9 | `make check-submission` |

Each `make analyze-*` writes a Markdown report (and, for stampede/hot-
key, a JSON artifact) under `perf/results/<label>/` that the review
template tells you how to cite.

## How the experiments work

- **Counters are diffed per run.** Every bench script snapshots the
  app's `/metrics` before and after the run (`app-metrics-before.txt`
  and `app-metrics.txt`); the analyzers subtract so each run reflects
  only its own traffic.
- **Fan-in ratio.** Defined as `source_fetches / expected_expiries`
  where `expected_expiries = duration / TTL`. The single hot key expires
  once per TTL window; without coalescing each expiry triggers hundreds
  of concurrent SoR fetches (fan-in in the hundreds), with singleflight/
  xfetch/swr it collapses toward 1. See `scripts/analyze-stampede.py`.
- **Hot-key imbalance.** `make inject-hot-key` records the key+weight;
  `make bench-hot-key` drives Zipf load with that fraction pinned to the
  key. Because the app shards by hash, all hot traffic hits one node —
  visible in `lab51_redis_ops_total{node=...}` and the per-shard panel.
- **Staleness race.** In `staleness` mode the loadgen runs writers that
  bump versions in the SoR and readers that read through the cache and
  compare each read to `/source`; mismatches count as stale and the
  worst gap feeds `max_staleness_seconds`.

## Observability

- Prometheus UI: <http://localhost:9090>
- Grafana: <http://localhost:3000> (anonymous Admin, light theme)
- Provisioned dashboard: **Cache Overview** — hit ratio, read p50/p99,
  fan-in ratio, source-fetch vs miss rate, per-node ops/sec, eviction
  rate, staleness rate, offered vs served.

## What to submit

- `docs/review.md` (filled from `docs/review.template.md`).
- `docs/staleness-policy.md` (filled from
  `docs/staleness-policy.template.md`).
- `perf/results/env.txt`, `perf/results/meta.json`.
- Every `perf/results/<label>/` run directory referenced in the review.
- Grafana screenshots in `docs/img/` cited from `docs/review.md`.

`make check-submission` parses `docs/review.md`, asserts every cited
filename exists, and warns on remaining `TODO` markers.

## Troubleshooting

- Port collisions: copy `.env.example` to `.env` and override the
  `LAB_*_PORT` values that conflict.
- "No data points" in Grafana: the dashboard uses recording rules; let
  it run for ~2 minutes after a bench starts.
- Empty hit ratio at first: run `make seed` (warms the cache) before
  `make bench-baseline`.
- `make bench-hot-key` errors with "no active hot key": run
  `make inject-hot-key KEY=... WEIGHT=...` first.
