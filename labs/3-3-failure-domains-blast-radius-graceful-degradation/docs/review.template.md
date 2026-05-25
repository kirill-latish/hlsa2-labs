# Architecture review - lab 3-3 (failure domains, blast radius, graceful degradation)

> Copy to `docs/review.md` and fill the TODOs.
> Target: ~1,500-2,000 words; every quantitative claim must cite a
> file under `perf/results/`. `make check-submission` enforces this.

---

## 1. Environment & method

- **Captured**: `perf/results/env.txt` (and `perf/results/env.json`)
- **Workload model**: `perf/workload.json` (baseline at TODO RPS for TODO s; overload ramp peaks at TODO RPS)
- **Lab version**: TODO (git rev) - see `perf/results/env.txt`
- **Hosts**: TODO (single laptop / VM / etc.)

Method: 3 runs per labelled condition (baseline, faulted-before,
faulted-after, storm, tamed) using `make bench-baseline RUNS=3` and
`make bench-overload`. Decisions use the 2-sigma rule from
`scripts/analyze-compare.py`.

## 2. Failure domains & dependency classification

Citation: `docs/failure-domains.md` (filled from
`docs/failure-domains.template.md`).

Summary:

- Critical deps: `price`, `cart` (cannot degrade; outage MUST fail the request)
- Non-critical deps: `recommendations`, `reviews`, `recently-viewed`
  (can degrade via LKG cache or omit the widget)
- Process boundaries: TODO (each dep is its own container, single
  failure domain; the gateway is a separate failure domain)
- Why this matters: TODO (one sentence on why mixing critical and
  non-critical behind one HTTP pool is the antipattern the topic
  teaches)

## 3. Healthy baseline (3 runs)

Citation: `perf/results/baseline/report.md`

| metric                          | median | sigma |
| ------------------------------- | -----: | ----: |
| critical-journey success ratio  | TODO % | TODO  |
| p50 latency (ms)                | TODO   | TODO  |
| p95 latency (ms)                | TODO   | TODO  |
| p99 latency (ms)                | TODO   | TODO  |

Run logs: `perf/results/baseline/run1/`, `run2/`, `run3/`.

## 4. Unprotected blast radius

Setup: `make inject-fault DEP=recommendations MODE=down` then
`make bench-baseline LABEL=faulted-before RUNS=3` (controls all off).

Citation: `perf/results/faulted-before/report.md`.

- Success ratio fell from TODO% to TODO% (delta = TODO)
- The fault was on **one non-critical dep** (`recommendations`).
- Per-dep success ratios show TODO (which deps were affected, even
  though only recommendations was faulted - this is the "shared fate
  via shared pool" story).
- Grafana screenshot: `docs/img/unprotected-blast-radius.png`

## 5. Isolation / graceful degradation (before vs after)

Setup: `make bench-baseline LABEL=faulted-after RUNS=3 BULKHEAD=on
CIRCUIT_BREAKER=on FALLBACK=on` (same fault).

Citation: `perf/results/faulted-after/report.md`,
`perf/results/faulted-after/compare-vs-before.md`.

Identical-fault verified by `scripts/compare.sh` (both runs cite the
same `perf/results/active-fault.txt`).

| metric                       | before | after  | delta  |
| ---------------------------- | -----: | -----: | -----: |
| success ratio                | TODO % | TODO % | TODO   |
| p99 latency (ms)             | TODO   | TODO   | TODO   |
| fallbacks served (per minute)| TODO   | TODO   | TODO   |

Decision (2-sigma rule on success_ratio): TODO

Mechanisms that mattered:

- **`BULKHEAD=on`** TODO (one paragraph: did per-dep pool isolation
  protect the critical path? quantify pool utilization for the slow
  dep vs the healthy deps.)
- **`CIRCUIT_BREAKER=on`** TODO
- **`FALLBACK=on`** TODO (cite `lab33_gateway_fallbacks_served_total`)

## 6. Load-dependent failure and mitigation

Setup: `make bench-overload LABEL=storm RETRY_BUDGET=off LOAD_SHED=off`
followed by `make bench-overload LABEL=tamed RETRY_BUDGET=on LOAD_SHED=on`.

Citation: `perf/results/overload/report.md`.

- Storm: offered=TODO rps, served=TODO rps, retry_amp=TODO
- Tamed: offered=TODO rps, served=TODO rps, retry_amp=TODO
- The "offered vs served" gap and retry-amplification ratio are the
  metastable-loop signature the topic teaches.
- Grafana screenshot: `docs/img/storm-vs-tamed.png`

## 7. Decision-tree justification + residual risks

Decision tree for "which control to enable":

- If a non-critical dep can produce stale-but-OK data -> **FALLBACK**
- If a non-critical dep is fully unavailable -> **FALLBACK** + omit
- If shared pool saturation is observed -> **BULKHEAD**
- If a dep is in a known-bad state -> **CIRCUIT_BREAKER**
- If load > capacity and clients retry -> **RETRY_BUDGET** + **LOAD_SHED**

Residual risks the lab does NOT cover:

- Multi-region / DNS-level isolation
- Database-level failure domains
- Long-tail timeouts under garbage collection / kernel pauses
- Inter-service auth failures

Runbooks: `runbooks/blast-radius-incident.md`, `runbooks/retry-storm.md`.

---

## Reproducibility note

The full pipeline runs from a clean checkout with:

```
cp .env.example .env
make up && make env-fingerprint && make show-topology
make bench-baseline RUNS=3
make inject-fault DEP=recommendations MODE=down
make bench-baseline LABEL=faulted-before RUNS=3
make bench-baseline LABEL=faulted-after RUNS=3 BULKHEAD=on CIRCUIT_BREAKER=on FALLBACK=on
make compare BEFORE=faulted-before AFTER=faulted-after
make clear-fault DEP=recommendations
make bench-overload LABEL=storm RETRY_BUDGET=off LOAD_SHED=off
make bench-overload LABEL=tamed RETRY_BUDGET=on LOAD_SHED=on
make analyze-overload
make check-submission
```
