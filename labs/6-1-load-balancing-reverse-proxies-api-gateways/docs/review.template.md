# Architecture review - lab 6-1 (load balancing, reverse proxies, API gateways)

> Copy to `docs/review.md` and fill the TODOs.
> Target: ~1,500-2,000 words; every quantitative claim must cite a file
> under `perf/results/` or `docs/img/`. `make check-submission` enforces
> that every cited filename exists.

For each experiment answer the four questions: **what did you measure**
(cite the artifact filename), **what changed**, **why** (the mechanism),
and **what new risk** the change introduces.

---

## 1. Environment & method

- **Captured**: `perf/results/env.txt` (and `perf/results/meta.json`)
- **Edge tier**: instrumented Go reverse proxy (see README for the
  proxy-family decision); 4 backends; shared Postgres dependency.
- **Workload model**: `perf/workload.json` (baseline at TODO RPS for
  TODO; uneven-cost distribution at TODO% slow).
- **Lab version**: TODO (git rev) - see `perf/results/env.txt`.
- **Hosts**: TODO (single laptop / VM / etc.).

Method: 3 runs for the baseline (`make bench-baseline RUNS=3`); single
labelled runs for each injected scenario, snapshotting artifacts to
`perf/results/<label>/`.

## 2. Baseline edge overhead (separate from backend latency)

Citation: `perf/results/baseline/report.md`

| metric                | median | sigma |
| --------------------- | -----: | ----: |
| EDGE overhead p50 (ms)| TODO   | TODO  |
| EDGE overhead p99 (ms)| TODO   | TODO  |
| total p50 (ms)        | TODO   | TODO  |
| total p99 (ms)        | TODO   | TODO  |
| throughput (rps)      | TODO   | TODO  |

- The edge adds TODO ms (p50) of pure overhead, which is TODO% of the
  total. **Why this matters**: total = edge overhead + network + backend
  processing; teams report one "p99" and never isolate the edge.
- Run logs: `perf/results/baseline/run1/`, `run2/`, `run3/`.
- Screenshot: `docs/img/baseline-overhead.png`

## 3. Load distribution under two algorithms

Setup: `make bench-distribution ALGO=round-robin LABEL=dist-rr` then
`ALGO=least-conn LABEL=dist-lc`, then
`make compare-distribution BEFORE=dist-rr AFTER=dist-lc`.

Citation: `perf/results/distribution/dist-rr/report.md`,
`perf/results/distribution/dist-lc/report.md`,
`perf/results/distribution/compare-distribution.md`.

| algorithm   | max-vs-mean skew | note                          |
| ----------- | ---------------: | ----------------------------- |
| round-robin | TODO             | uniform by count, ignores load|
| least-conn  | TODO             | adapts count to live load     |

- **Why**: round-robin distributes by request count and ignores backend
  capacity, so the slow backend (`backend-4`) backs up; least-conn
  routes on live in-flight count and rebalances the uneven-cost workload.
- **New risk**: TODO (least-conn can herd onto a backend that is fast
  only because it is erroring quickly).
- Screenshot: `docs/img/distribution.png`

## 4. Failover under induced failure + health-check tuning

Setup: `make inject-backend-failure BACKEND=backend-2 LABEL=failover-baseline`,
analyze, restore, `make apply-fix CANDIDATE=fast-healthcheck INTERVAL=2s THRESHOLD=2`,
re-inject as `failover-after`, then
`make compare-failover BEFORE=failover-baseline AFTER=failover-after`.

Citation: `perf/results/failover-baseline/report.md`,
`perf/results/failover-after/report.md`,
`perf/results/failover-after/compare-vs-before.md`.

| metric                 | before | after |
| ---------------------- | -----: | ----: |
| detection time (s)     | TODO   | TODO  |
| dropped (502)          | TODO   | TODO  |

- **Why**: detection time = health-check interval x failure threshold.
- **New risk**: faster checks raise check load on backends and the risk
  of flapping on a marginal backend. TODO.

## 5. Deep vs shallow health-check cascading failure

Setup: `make apply-fix CANDIDATE=deep-healthcheck`,
`make inject-dependency-hiccup DURATION=5s LABEL=healthcheck-deep`; then
`make apply-fix CANDIDATE=shallow-healthcheck`,
`make inject-dependency-hiccup DURATION=5s LABEL=healthcheck-shallow`;
then `make compare-healthcheck`.

Citation: `perf/results/healthcheck-deep/report.md`,
`perf/results/healthcheck-shallow/report.md`,
`perf/results/healthcheck-shallow/compare-vs-deep.md`.

- Deep: min healthy backends -> TODO (0 = full outage), 503 count TODO.
- Shallow: min healthy backends -> TODO (stayed in rotation), 503 TODO.
- **Why**: a deep check verifies the shared dependency, so a brief blip
  fails the check on ALL backends simultaneously -> zero healthy ->
  503. A shallow check only verifies the process is up.
- **New risk**: shallow checks miss a process that is up but genuinely
  broken downstream. TODO.
- Screenshots: `docs/img/healthcheck-deep.png`, `docs/img/healthcheck-shallow.png`

## 6. 5xx classification (502 / 503 / 504)

Setup: `make inject-5xx-scenarios LABEL=5xx` then `make analyze-5xx LABEL=5xx`.

Citation: `perf/results/5xx/report.md`.

| code | scenario count | layer signal              |
| ---- | -------------: | ------------------------- |
| 502  | TODO           | connectivity              |
| 503  | TODO           | capacity / health         |
| 504  | TODO           | backend latency           |

- **Why each maps to a layer**: 502 = proxy could not connect; 503 = no
  healthy backends; 504 = backend reached but too slow.
- Screenshot: `docs/img/5xx-by-class.png`

## 7. Decision-ladder justification + residual risks

Use the topic's edge decision ladder to justify the architecture you'd
recommend:

> no edge -> managed LB -> single reverse proxy -> reverse proxy +
> lightweight gateway -> dedicated gateway -> multi-tier + mesh

- Recommended rung for this service: TODO (and why, citing the overhead
  and failover numbers above).
- Residual risks for the production runbook:
  - TODO (e.g. multi-AZ LB failure domain)
  - TODO (TLS termination + connection draining on deploy)
  - TODO (health-check depth review per dependency)

Runbooks: `runbooks/failover-incident.md`,
`runbooks/healthcheck-cascade-incident.md`.

---

## Reproducibility note

```
cp .env.example .env
make up && make env-fingerprint && make seed
make bench-baseline RUNS=3 DURATION=5m && make analyze-baseline
make bench-distribution ALGO=round-robin DURATION=3m LABEL=dist-rr
make bench-distribution ALGO=least-conn  DURATION=3m LABEL=dist-lc
make compare-distribution BEFORE=dist-rr AFTER=dist-lc
make inject-backend-failure BACKEND=backend-2 LABEL=failover-baseline
make analyze-failover LABEL=failover-baseline
make restore-backend BACKEND=backend-2
make apply-fix CANDIDATE=fast-healthcheck INTERVAL=2s THRESHOLD=2
make inject-backend-failure BACKEND=backend-2 LABEL=failover-after
make analyze-failover LABEL=failover-after
make compare-failover BEFORE=failover-baseline AFTER=failover-after
make restore-backend BACKEND=backend-2
make apply-fix CANDIDATE=deep-healthcheck
make inject-dependency-hiccup DURATION=5s LABEL=healthcheck-deep
make analyze-healthcheck LABEL=healthcheck-deep
make apply-fix CANDIDATE=shallow-healthcheck
make inject-dependency-hiccup DURATION=5s LABEL=healthcheck-shallow
make analyze-healthcheck LABEL=healthcheck-shallow
make compare-healthcheck
make inject-5xx-scenarios LABEL=5xx && make analyze-5xx LABEL=5xx
make check-submission
```
