# HLSA2 Lab 3-3 — Failure Domains, Blast Radius, and Graceful Degradation

Companion lab for topic 246. You will:

1. Stand up a small e-commerce checkout topology with one **critical**
   path (price + cart) and three **non-critical** widgets
   (recommendations + reviews + recently-viewed).
2. Measure a healthy baseline (success + p50/p95/p99 across 3 runs).
3. Inject realistic faults (`down`, `latency`, `errors`) into a single
   non-critical dependency and measure the **blast radius** on the
   critical journey.
4. Turn on five resilience controls one at a time and prove with
   identical-fault before/after numbers which actually reduce blast
   radius.
5. Drive the gateway past saturation, watch the retry storm, then add
   `RETRY_BUDGET=on` + `LOAD_SHED=on` and watch the curve recover.
6. Write the architecture review and runbooks.

> The five controls are deliberately **off by default** so step 5's
> failure is real and step 6's improvement is real. Pass them on the
> `make` command line; `.env` is for host port overrides only.

## Prerequisites

- Docker + Docker Compose (Docker Desktop or equivalent).
- `make`, `bash`, `jq`, `python3` (3.10+), `curl` on the host.
- ~3 GB of free disk for images + Prometheus TSDB.
- Ports `3000`, `8080`, `8090-8095`, `9000`, `9090` free on the host
  (override via `.env` from `.env.example` if any clash).

## Stack overview

| Service           | Port | Role                                                     | Critical? |
| ----------------- | ---- | -------------------------------------------------------- | --------- |
| `gateway`         | 8080 | Fans out to all five deps; aggregates `/checkout` reply  | n/a       |
| `price`           | 8091 | Stock-keeping price lookup                               | yes       |
| `cart`            | 8092 | Authoritative cart contents                              | yes       |
| `recommendations` | 8093 | "You might also like" carousel                           | no        |
| `reviews`         | 8094 | Star rating + review preview                             | no        |
| `recently-viewed` | 8095 | Recently-viewed widget                                   | no        |
| `loadgen`         | 8090 | In-cluster Go HTTP load driver                           | n/a       |
| `fault-injector`  | 9000 | Central authoritative fault store; deps poll it          | n/a       |
| `prometheus`      | 9090 | Scrapes everything every 5s                              | n/a       |
| `grafana`         | 3000 | Provisioned Resilience Overview dashboard                | n/a       |

All Go services use one `go.mod` at the lab root and share helpers
under [`internal/`](internal/) (metrics, breaker, bulkhead, retry,
fallback, fault client).

## One-line bring-up

```bash
cp .env.example .env
make up
make env-fingerprint
make show-topology
```

You should see all containers healthy and a topology map citing
`price` + `cart` as critical and the rest as non-critical.

## Steps from the topic guide

| Step | Command(s)                                                                                       |
| ---- | ------------------------------------------------------------------------------------------------ |
| 1    | `make up`, `make ps`, `make logs`, `make down`                                                   |
| 2    | `make env-fingerprint`                                                                           |
| 3    | `make show-topology`                                                                             |
| 4    | `make bench-baseline RUNS=3`, `make analyze-baseline`                                            |
| 5    | `make inject-fault DEP=recommendations MODE=down`, `make bench-baseline LABEL=faulted-before`, `make analyze-blast-radius LABEL=faulted-before` |
| 6    | `make bench-baseline LABEL=faulted-after BULKHEAD=on CIRCUIT_BREAKER=on FALLBACK=on`, `make compare BEFORE=faulted-before AFTER=faulted-after` |
| 7    | `make bench-overload RETRY_BUDGET=off LOAD_SHED=off LABEL=storm`, `make bench-overload RETRY_BUDGET=on LOAD_SHED=on LABEL=tamed`, `make analyze-overload` |
| 8    | Fill in `docs/review.md` from `docs/review.template.md`                                          |
| 9    | `make check-submission`                                                                          |

Each `make analyze-*` writes a Markdown report under
`perf/results/<label>/report.md` that the review template tells you
how to cite.

## Resilience controls (the five toggles)

All five default to `off`. Override per invocation:
`make bench-baseline LABEL=… BULKHEAD=on CIRCUIT_BREAKER=on`. Each
bench script POSTs the requested flag set to `gateway:/admin/config`
**before** issuing load, so a single `make up` is enough — you don't
restart the gateway between conditions. Use
`curl http://localhost:8080/admin/config` to read the active flags at
any time.

- **`BULKHEAD=on`** — separate `*http.Transport` per dep so a saturated
  non-critical pool can't evict critical-path connections. See
  [`internal/bulkhead/`](internal/bulkhead/).
- **`CIRCUIT_BREAKER=on`** — per-dep tripped-on-failures breaker, ported
  from lab 3-2. See [`internal/breaker/`](internal/breaker/).
- **`FALLBACK=on`** — non-critical deps fall back to last-known-good
  cached value on error or omit the widget if no LKG yet. Critical
  deps still fail the request. See [`internal/fallback/`](internal/fallback/).
- **`RETRY_BUDGET=on`** — retries enabled but capped by a global
  token-bucket budget at 10% of inbound RPS. See [`internal/retry/`](internal/retry/).
- **`LOAD_SHED=on`** — `429` when in-flight exceeds the ceiling;
  per-dep `503` when that dep's pool is at capacity.

## Observability

- Prometheus UI: <http://localhost:9090>
- Grafana: <http://localhost:3000> (anonymous Admin)
- Provisioned dashboard: **Resilience Overview** — four rows:
  critical-path success + p99, per-dep success + latency, pool/queue
  depth, retry rate + offered-vs-served (the metastable-loop panel).

## What to submit

After the runs you should have:

- `docs/review.md` (filled from `docs/review.template.md`).
- `docs/failure-domains.md` (filled from `docs/failure-domains.template.md`).
- `perf/results/env.txt`, `perf/results/<label>/meta.json` for every
  labelled run.
- Two grafana screenshots in `docs/img/` cited from `docs/review.md`.
- One annotated runbook copied from `runbooks/`.

`make check-submission` parses `docs/review.md`, asserts every cited
filename exists, and warns on remaining `TODO` markers.

## Troubleshooting

- Port collisions: copy `.env.example` to `.env` and override the
  `LAB_*_PORT` values that conflict.
- `docker compose ps` stalls in `health: starting`: services rely on
  the fault-injector being healthy first; let it warm up for ~15s.
- Prometheus shows no data: confirm `gateway`/`loadgen`/deps are all
  in `running (healthy)` — Prometheus only scrapes healthy containers.
- "No data points" in Grafana panels: the dashboard uses recording
  rules; let it run for 2 minutes after `make bench-baseline` starts.
