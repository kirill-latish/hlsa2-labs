# HLSA2 Lab 6-1 — Load Balancing, Reverse Proxies, and API Gateways

Companion lab for topic 253. You will run the edge tier as a benchmark
and ship edge-tier changes with evidence, not intuition:

1. Stand up an edge topology: an **edge proxy** in front of **4
   backends**, a shared **Postgres** dependency, a **load driver**, and
   a provisioned **Prometheus + Grafana** pair.
2. Measure the **edge latency overhead** *separately* from backend
   processing time — the number teams never isolate.
3. Compare **load distribution** under round-robin vs least-conn with a
   deliberately uneven request cost.
4. Induce a **backend failure** and measure failover detection time and
   dropped requests, then tune the health check.
5. Reproduce the **deep-health-check cascading failure** (a brief
   dependency blip taking the whole service down), then prove a shallow
   check rides it out.
6. Read **502 / 503 / 504** as layer signals.
7. Write the architecture review and runbooks.

## Why a Go reverse proxy (and not NGINX / Envoy / HAProxy)?

**Decision:** the edge proxy in this lab is an **instrumented Go reverse
proxy** (`cmd/edge-proxy`), matching the repo style (lab 3-3's gateway
is also Go).

**Why:** the lab's whole point is to *measure* edge behaviour. A Go
proxy lets us emit a Prometheus metric for every quantity the topic
guide asks about, with no sidecar log-scraping:

- the **edge-overhead span timing**, computed by subtracting the
  backend's self-reported `X-Backend-Process-Ms` from the total edge
  time (`lab61_edge_overhead_seconds`);
- the **balancing algorithm** (round-robin / least-conn), switchable at
  runtime via `POST /admin/config`;
- the **health-check depth** knob (shallow vs deep) and the active
  health-check loop with a tunable interval + failure threshold;
- crisp **5xx classification** (502 connectivity / 503 no-healthy /
  504 timeout).

**The production option (documented, not used here):** in production you
would usually reach for a managed load balancer or a mature proxy —
**NGINX**, **Envoy**, or **HAProxy** — which give you battle-tested
connection handling, TLS, and L4/L7 features for free. The trade-off is
that you then measure them through *their* metrics/log formats. The
section 7 decision ladder in the review asks you to justify which rung
fits a real service.

## Prerequisites

- Docker + Docker Compose (Docker Desktop or equivalent).
- `make`, `bash`, `jq`, `python3` (3.10+), `curl` on the host.
- ~3 GB of free disk for images + Prometheus TSDB + Postgres volume.
- Ports `3000`, `5432`, `8080-8084`, `8090`, `9090` free on the host
  (override via `.env` from `.env.example` if any clash).

## Stack overview

| Service        | Port | Role                                                       |
| -------------- | ---- | ---------------------------------------------------------- |
| `edge-proxy`   | 8080 | Instrumented Go reverse proxy; routing + health + 5xx      |
| `backend-1`    | 8081 | App instance (fast)                                        |
| `backend-2`    | 8082 | App instance (fast)                                        |
| `backend-3`    | 8083 | App instance (fast)                                        |
| `backend-4`    | 8084 | App instance (deliberately slower per-instance)            |
| `postgres`     | 5432 | Shared dependency for the deep-health-check cascade        |
| `loadgen`      | 8090 | In-cluster Go load driver (fast/slow request mix)          |
| `prometheus`   | 9090 | Scrapes everything every 5s                                |
| `grafana`      | 3000 | Provisioned **Edge Overview** dashboard (anonymous Admin)  |

All Go services share one `go.mod` at the lab root and the helpers under
[`internal/`](internal/) (`metrics`, `proxy`). Each backend uses the
same binary; only `BACKEND_NAME` and `BASE_LATENCY_MS` differ.
`backend-4` is slower so round-robin (count-uniform) overloads it while
least-conn routes around it.

## One-line bring-up

```bash
cp .env.example .env
make up
make env-fingerprint
make seed
make edge-status
```

You should see all containers healthy, `make seed` routing across all 4
backends, and `make edge-status` reporting `healthy_backends: 4`.

## Steps from the topic guide

| Step | Command(s)                                                                                   |
| ---- | ------------------------------------------------------------------------------------------- |
| 1    | `make up`, `make ps`, `make seed`, `make edge-status`                                        |
| 2    | `make env-fingerprint`                                                                       |
| 3    | `make bench-baseline RUNS=3 DURATION=5m`, `make analyze-baseline`                            |
| 4    | `make bench-distribution ALGO=round-robin DURATION=3m LABEL=dist-rr`, `... ALGO=least-conn LABEL=dist-lc`, `make compare-distribution BEFORE=dist-rr AFTER=dist-lc` |
| 5    | `make inject-backend-failure BACKEND=backend-2 LABEL=failover-baseline`, `make analyze-failover LABEL=failover-baseline`, `make restore-backend BACKEND=backend-2`, `make apply-fix CANDIDATE=fast-healthcheck INTERVAL=2s THRESHOLD=2`, re-inject as `failover-after`, `make compare-failover BEFORE=failover-baseline AFTER=failover-after` |
| 6    | `make apply-fix CANDIDATE=deep-healthcheck`, `make inject-dependency-hiccup DURATION=5s LABEL=healthcheck-deep`, `make analyze-healthcheck LABEL=healthcheck-deep`, `make apply-fix CANDIDATE=shallow-healthcheck`, `... LABEL=healthcheck-shallow`, `make compare-healthcheck` |
| 7    | `make inject-5xx-scenarios LABEL=5xx`, `make analyze-5xx LABEL=5xx`                          |
| 8    | Fill in `docs/review.md` from `docs/review.template.md`                                      |
| 9    | `make check-submission`                                                                      |

Each `make analyze-*` / `compare-*` writes a Markdown report under
`perf/results/<label>/` that the review template tells you how to cite.

## How the edge measures overhead

Each backend stamps its own measured processing time into the
`X-Backend-Process-Ms` response header. The edge records the **total**
request time it observed and subtracts the backend's number, leaving the
**pure edge overhead** (routing decision + proxy + network + body copy):

```
overhead = total_edge_time - backend_process_time   ->  lab61_edge_overhead_seconds
```

`make analyze-baseline` reports edge overhead p50/p99 *separately* from
total latency, with run-to-run sigma.

## Runtime configuration (no restarts between conditions)

The edge is reconfigured at runtime via `POST /admin/config`; the bench
scripts and `make apply-fix` do this for you, so a single `make up` is
enough.

- **Algorithm**: `{"algo":"round-robin"|"least-conn"}` (also set by
  `make bench-distribution ALGO=...`).
- **Health check**: `{"health_depth":"shallow"|"deep",
  "health_interval_ms":2000,"failure_threshold":2}` (set by
  `make apply-fix`).
- **Fault injection**: `make inject-backend-failure` forwards the fault
  to the backend so the proxy still has to *detect* it via health
  checks; `make inject-dependency-hiccup` pauses Postgres; the 502/503/
  504 scenarios are driven by `make inject-5xx-scenarios`.

Read the live state any time with `make edge-status`
(`GET /admin/status`).

## Observability

- Prometheus UI: <http://localhost:9090>
- Grafana: <http://localhost:3000> (anonymous Admin)
- Provisioned dashboard: **Edge Overview** — edge latency overhead,
  per-backend distribution, backend health state, in-flight connections,
  5xx by class, offered-vs-served, and the request-skew ratio.

## Metrics vocabulary (`lab61_*`)

| Metric                               | Meaning                                  |
| ------------------------------------ | ---------------------------------------- |
| `lab61_edge_overhead_seconds`        | edge overhead (the headline number)      |
| `lab61_edge_request_duration_seconds`| total edge-observed request duration     |
| `lab61_edge_backend_requests_total`  | per-backend request distribution counter |
| `lab61_edge_backend_up`              | backend health state (1/0)               |
| `lab61_edge_backend_inflight`        | in-flight requests per backend           |
| `lab61_edge_5xx_total`               | 5xx by class (502/503/504)               |
| `lab61_edge_healthcheck_fail_total`  | failed health checks by backend + depth  |
| `lab61_loadgen_offered_total` / `..._served_total` | offered vs served            |

## What to submit

- `docs/review.md` (filled from `docs/review.template.md`).
- `perf/results/env.txt`, `perf/results/meta.json`.
- Every `perf/results/<label>/` run directory referenced in the review.
- Screenshots under `docs/img/` cited from `docs/review.md`.
- The two runbooks under `runbooks/`.

`make check-submission` parses `docs/review.md`, asserts every cited
filename exists, and warns on remaining `TODO` markers.

## Troubleshooting

- Port collisions: copy `.env.example` to `.env` and override the
  `LAB_*_PORT` values that conflict.
- `make edge-status` shows fewer than 4 healthy backends right after
  `make up`: give the backends + Postgres ~15s to pass their first
  health checks.
- "No data points" in Grafana: the dashboard uses recording rules; let
  it run for ~1-2 minutes after a bench starts.
- After the 5xx or failover experiments, run `make restore-backend
  BACKEND=backend-N` (or re-run a clean bench) to return the stack to a
  healthy state.
