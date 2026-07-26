# Lab 2-2: RED, USE, SLIs, SLOs, and Alert Quality

A working observability stack you bring up locally, instrument with a
real **SLO**, and prove out with three controlled load-generator
experiments. The deliverables are a defensible SLO design, the
recording/alerting rules behind it, evidence from a fast-burn outage and
a slow-burn degradation, and a written architecture review.

The infrastructure is provided. You write:

- `docs/slo.md` — SLI/SLO design (deliverable id=430)
- `prometheus/rules/slo.yml` — three-layer recording + alerting rules
- `alertmanager/alertmanager.yml` (already templated; you adjust receivers)
- `docs/review.md` — architecture review (deliverable id=433)

## Prerequisites

- Docker 24+ and Docker Compose v2
- Python 3.12+ (only needed if you want a host-side venv for ad-hoc work;
  all services run inside containers)
- Roughly 4 GB free RAM and a few GB free disk for Prometheus' local TSDB

## Stack overview

| Service        | Port  | What it does                                                              |
|----------------|-------|---------------------------------------------------------------------------|
| `checkout`     | 8080  | The system under SLO. FastAPI, exposes `POST /checkout` and `/metrics`.  |
| `payments`     | 8081  | Downstream stub. Honors `POST /admin/inject` for fault injection.         |
| `loadgen`      | 9999  | Drives offered load and toggles fault injection per profile.              |
| `prometheus`   | 9090  | Scrapes the three services. Loads `prometheus/rules/*.yml`.               |
| `alertmanager` | 9093  | Routes alerts (`severity: page` / `severity: ticket`).                    |
| `grafana`      | 3000  | Provisioned with one starter dashboard (`RED + Burn Rate Overview`).      |

## Setup

```bash
cd labs/2-2-red-use-sli-slo-alert-quality/
docker compose up -d
```

If host port 8080 is already taken on your machine, set `CHECKOUT_PORT` in
a `.env` file in this directory (e.g. `CHECKOUT_PORT=18080`) and use that
port in place of 8080 below. Only the host binding changes; in-cluster
URLs and Prometheus targets are unaffected.

Wait ~30 seconds for healthchecks to pass, then verify each service:

```bash
curl -s http://localhost:8080/healthz       # checkout
curl -s http://localhost:8081/healthz       # payments
curl -s http://localhost:9090/-/ready       # prometheus
curl -s http://localhost:9093/-/ready       # alertmanager
curl -s http://localhost:3000/api/health    # grafana
```

Open Grafana at <http://localhost:3000> (anonymous viewer is enabled, or
log in as `admin / admin`). The starter dashboard is provisioned at
**Dashboards > RED + Burn Rate Overview** (`/d/red-overview/red-overview`).

The `loadgen` container starts in `smoke` mode by default (60 s of
nominal load), so the panels populate as soon as the stack is healthy.

## Running an experiment

```bash
bash scripts/run-experiment.sh nominal       # 30-min steady-state baseline
bash scripts/run-experiment.sh fast-burn     # 12-min, 5% errors
bash scripts/run-experiment.sh slow-burn     # 90-min, 0.7% errors
bash scripts/run-experiment.sh latency-burn  # 15-min, 400ms injected latency
```

Each run:

1. Confirms the stack is up.
2. Captures a "before" Prometheus snapshot into the artifact directory.
3. POSTs the profile's fault-injection payload to
   `http://payments:8081/admin/inject`.
4. Drives `rps` requests/second against `POST /checkout` for the
   profile's `duration_seconds`.
5. Resets payments to baseline.
6. Captures an "after" snapshot, waits `RANGE_WAIT` seconds (default 300)
   for alert decay, then captures the range series.
7. Saves `loadgen` stdout and a small meta.json under
   `artifacts/01-baseline/` (for `nominal`/`smoke`) or
   `artifacts/02-experiments/` (for the fault profiles).

**Experiment order matters.** These alerts read `rate()` over windows up
to 6h, so a run inherits whatever traffic preceded it. Running the clean
baseline before `slow-burn` dilutes the 6h window enough that the
slow-burn alert never fires, even though the alert is correct. Use
`slow-burn → nominal → (1h gap) → fast-burn → latency-burn`, and leave a
gap of at least the longest window between runs whose windows must not
overlap. The reasoning is worked out in `docs/review.md` section 3.

### Evidence capture

`scripts/capture-metrics.sh` is what makes the runs auditable after
Prometheus' 2-day retention rolls over:

```bash
bash scripts/capture-metrics.sh snapshot artifacts/01-baseline my-tag
bash scripts/capture-metrics.sh range artifacts/01-baseline my-tag <start> <end>
bash scripts/capture-metrics.sh cardinality artifacts
```

TTD/TTR are read from the `ALERTS` metric in the range capture, which
exists only while an alert is pending or firing — so detect and reset
times are measured, not inferred from wall-clock guesses.

You can also run a profile directly without the wrapper:

```bash
docker compose run --rm loadgen python -m loadgen fast-burn
```

## What you write

### 1. `docs/slo.md`

Define the **availability SLI** and the **latency SLI** for
`POST /checkout`. Each must specify numerator, denominator, exclusion
rules, measurement point, and the SLO target with baseline justification.
The latency SLI must be **threshold-style**, not percentile-style — the
checkout histogram has a bucket boundary at exactly `0.3 s` so a
threshold-style SLI is exact, not approximate. Lay out the rejected
alternatives and why you rejected each.

### 2. `prometheus/rules/slo.yml`

Three layers (already scaffolded with a worked availability example):

- **Layer A** — short-window SLI ratios for 5m / 30m / 1h / 6h.
- **Layer B** — burn rate `(1 - SLI) / (1 - SLO)` for each window.
- **Layer C** — multi-window multi-burn-rate alerts:
  - `SLOFastBurnAvailability` — 1h ≥ 14.4× **AND** 5m ≥ 14.4× → page
  - `SLOSlowBurnAvailability` — 6h ≥ 6× **AND** 30m ≥ 6× → ticket
  - `SLOFastBurnLatency` — same shape, on the latency SLI
  - `SLOSlowBurnLatency` — same shape, on the latency SLI

Every alert must carry: `summary`, `description` (with the live burn
rate), `runbook_url` (point at `runbooks/checkout-error-budget.md` in
your fork), and `dashboard_url` (point at the Grafana dashboard above).

After editing, reload Prometheus without restarting it:

```bash
curl -X POST http://localhost:9090/-/reload
curl -s http://localhost:9090/api/v1/rules | python3 -m json.tool | head -40
```

### 3. `alertmanager/alertmanager.yml`

A working template ships in this directory. The default webhook receiver
is `http://host.docker.internal:5001/`. The simplest local sink is:

```bash
docker run --rm -p 5001:80 mendhak/http-https-echo:33
```

Watch its stdout while running the fast-burn experiment to see real
alert payloads (with your annotations).

### 4. `docs/review.md`

The full architecture review (deliverable id=433). Cover SLI design and
rejected alternatives, burn-rate math, the three-experiment TTD/TTR
table, false-positive analysis, your written error-budget policy, the
cardinality audit table (kept vs rejected labels), and the 10×-growth
analysis.

## Histogram bucket boundaries

The checkout service's latency histogram is fixed at:

```
[0.05, 0.1, 0.2, 0.3, 0.5, 0.8, 1.0, 1.5, 2.0, 3.0, 5.0]
```

The boundary at `0.3 s` is deliberate: it matches a 300 ms latency SLO
threshold, so a threshold-style SLI of the form
`requests_under_300ms / total_requests` is **exact**. If you choose a
different threshold for your latency SLO, you will need to add a bucket
at that boundary in `checkout/main.py` and re-build the image.

## Cardinality

The checkout service ships exactly three labels on the RED metrics:
`route` (router template, never the instantiated path), `method` (HTTP
verb), `status_class` (the four-value enum `2xx / 3xx / 4xx / 5xx`).
Anything beyond that — `user_id`, `request_id`, `trace_id`, raw URL
paths — belongs in logs and traces, not metrics. Document this in
Section 6 of `docs/review.md`.

## Tear-down

```bash
docker compose down -v       # also clears volumes
```

Re-up is idempotent; `artifacts/` is preserved (it is committed by you).

## Project layout

```
labs/2-2-red-use-sli-slo-alert-quality/
  docker-compose.yml
  requirements.txt
  README.md                 ← you are here
  checkout/                 ← FastAPI service under SLO
    Dockerfile
    main.py
  payments/                 ← downstream stub with fault-injection admin
    Dockerfile
    main.py
  loadgen/                  ← profile-driven load generator
    Dockerfile
    main.py
    __init__.py
    __main__.py
    profiles.yaml
  prometheus/
    prometheus.yml
    rules/
      slo.yml               ← YOU WRITE THE LATENCY HALF
  alertmanager/
    alertmanager.yml        ← already wired; adjust receivers as needed
  grafana/
    provisioning/
      datasources/
        datasources.yml
      dashboards/
        dashboards.yml
    dashboards/
      red-overview.json     ← starter dashboard, extend or replace
  docs/
    slo.md                  ← DELIVERABLE id=430
    review.md               ← DELIVERABLE id=433
  runbooks/
    checkout-error-budget.md
  scripts/
    run-experiment.sh
    capture-metrics.sh      ← Prometheus evidence capture (snapshot/range/cardinality)
  artifacts/
    01-baseline/            ← nominal/smoke runs land here
    02-experiments/         ← fault-injection runs land here
    cardinality.json        ← TSDB series counts for the audit
```

Note: `.gitignore` ignores `*.log` globally but re-includes
`artifacts/**/*.log`, because the experiment logs are deliverables.

## Submitting

Push to a public fork and submit the repository URL. Verify on a clean
clone that:

- `docker compose up -d` is enough to bring everything healthy.
- `bash scripts/run-experiment.sh smoke` produces traffic and the
  Grafana dashboard panels populate.
- `prometheus/rules/slo.yml` validates with `promtool` (Prometheus
  refuses to load a broken file; check `docker compose logs prometheus`).
- All four alerts (`SLOFastBurnAvailability`, `SLOSlowBurnAvailability`,
  `SLOFastBurnLatency`, `SLOSlowBurnLatency`) are listed at
  `http://localhost:9090/alerts`.
- `docs/slo.md` and `docs/review.md` are filled in (no `TODO` markers).
- `artifacts/01-baseline/` and `artifacts/02-experiments/` are populated.
