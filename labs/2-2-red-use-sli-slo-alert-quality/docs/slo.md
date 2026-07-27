# SLO design for `POST /checkout`

> **Deliverable** — homework deliverable **id=430**, "SLO design document".

Two SLIs are defined for `POST /checkout`: one availability, one latency.
Both are event-based ratios of the form `good_events / valid_events`, both
are measured server-side, and both carry a 99.9% target over a 30-day
rolling window. The rules that implement them live in
`prometheus/rules/slo.yml`; the alerts they drive are described in
`docs/review.md`.

## 1. Service under measurement

- Service: `checkout` (FastAPI, `checkout/main.py`).
- Endpoint under SLO: `POST /checkout`.
- Measurement point: the `checkout` service's own `/metrics` endpoint,
  recorded by the RED middleware at `checkout/main.py:78-112`.
- Instrumented labels: `route` (router template), `method`,
  `status_class` (4-value enum). Nothing else — see the cardinality
  audit in `docs/review.md` section 6.

The middleware excludes `/healthz`, `/metrics` and `/` from the counters
entirely (`checkout/main.py:73-75`), so probe and scrape traffic never
enters either SLI's denominator. This is an exclusion applied *at
instrumentation time* rather than in the SLI expression, which is the
stronger form: it cannot be forgotten by a future query author.

## 2. Availability SLI

| Field | Value |
|---|---|
| **Definition** | fraction of valid `POST /checkout` requests answered successfully |
| **Numerator** (`good_events`) | `sum(rate(http_requests_total{service="checkout",route="/checkout",status_class=~"2xx\|3xx"}[<window>]))` |
| **Denominator** (`valid_events`) | `sum(rate(http_requests_total{service="checkout",route="/checkout",status_class=~"2xx\|3xx\|5xx"}[<window>]))` |
| **Excluded events** | `/healthz`, `/metrics`, `/` (excluded in the service middleware); **`4xx` excluded from both numerator and denominator** |
| **Measurement point** | Server-side, checkout RED middleware |
| **Windows** | 5m, 30m, 1h, 6h (driven by the burn-rate alert geometry) |
| **SLO target** | **99.9%** over 30 rolling days |

### Why 4xx is excluded rather than counted as bad

A `4xx` from this endpoint is the payments stub declining the charge and
checkout passing that status through (`checkout/main.py:153-157`). The
request was routed, processed, and answered correctly; the answer was
"no". Counting declines as SLO violations would make the error budget a
function of customer card quality rather than of service health — a
marketing campaign that attracted more declining cards would "burn
budget" and eventually page an engineer who can do nothing about it.

Excluding it from the denominator too (rather than counting it as a
valid-but-good event) is the deliberate choice: a 4xx is not evidence
that the service is working *or* failing, so it should not dilute the
ratio in either direction. With a large enough 4xx volume, counting them
as good would mask a real 5xx problem.

**Known limitation:** `429` and `408` would be lumped into `4xx` by the
`status_class` enum, and both *are* server-side failures that should
count as bad. This service emits neither today, so the exclusion is
currently exact. If rate limiting is ever added, the SLI must move to a
finer status label for those two codes specifically — recorded here so
the assumption is visible rather than implicit.

## 3. Latency SLI

| Field | Value |
|---|---|
| **Definition** | fraction of `POST /checkout` requests served faster than the threshold |
| **Numerator** (`good_events`) | `sum(rate(http_request_duration_seconds_bucket{service="checkout",route="/checkout",le="0.3"}[<window>]))` |
| **Denominator** (`valid_events`) | `sum(rate(http_request_duration_seconds_count{service="checkout",route="/checkout"}[<window>]))` |
| **Excluded events** | `/healthz`, `/metrics`, `/` (excluded in the service middleware) |
| **Measurement point** | Server-side, checkout RED middleware |
| **Windows** | 5m, 30m, 1h, 6h |
| **Threshold** | **300 ms** |
| **SLO target** | **99.9%** of requests under 300 ms, over 30 rolling days |

### Why threshold-style and not percentile-style

The SLI is `requests_under_300ms / total_requests`, not an alert on
`histogram_quantile(0.99, ...) > 0.3`. Four independent reasons:

1. **A quantile cannot produce a burn rate.** Burn rate is
   `(1 - SLI) / (1 - SLO)` — it needs a *fraction of bad events*.
   "p99 = 350 ms" does not say what share of the month's budget has been
   spent, so there is no way to derive a 14.4× threshold from it. The
   entire multi-window alerting apparatus is unavailable.
2. **Quantiles do not aggregate.** The p99 of two windows is not the
   average of their p99s, and the p99 across three instances is not the
   average of theirs. Event ratios add and average correctly, which is
   what makes the 5m/30m/1h/6h family coherent.
3. **`histogram_quantile` interpolates.** It assumes a uniform
   distribution *within* the containing bucket and interpolates linearly.
   The returned number therefore depends on the bucket layout as much as
   on the data. A threshold-style SLI at an exact boundary counts
   observations and interpolates nothing.
4. **Heavy-tail tenants.** One pathological tenant issuing slow requests
   moves p99 sharply while affecting a small fraction of traffic. The
   threshold SLI reports exactly the fraction of requests harmed, which
   is the quantity the error budget is denominated in.

### Why the bucket boundary at the threshold matters

`checkout/main.py:38` fixes the buckets at
`[0.05, 0.1, 0.2, 0.3, 0.5, 0.8, 1.0, 1.5, 2.0, 3.0, 5.0]`. Because
`0.3` is an explicit boundary, `..._bucket{le="0.3"}` *is* the count of
requests under 300 ms — an exact integer, no estimation.

Had the boundary not existed, the count under 300 ms would have to be
interpolated between the `0.2` and `0.5` buckets, assuming requests are
spread uniformly across that 300 ms span. They are not: real latency
distributions are dense near the mode and sparse toward the tail, so
uniform interpolation systematically misestimates. That error would land
directly in the numerator of the SLI, and then get multiplied by 1000
(`1/(1-0.999)`) on its way into the burn rate. Choosing an SLO threshold
that is not a bucket boundary means committing to an SLI whose value is
partly an artifact of instrumentation — the fix is to add the bucket, not
to interpolate.

### Measurement caveat: no status class on the histogram

`http_request_duration_seconds` carries only `(route, method)` — there is
no `status_class` label (`checkout/main.py:46-51`). Two consequences:

- The latency SLI cannot exclude 4xx the way the availability SLI does.
- Fast failures count as *good* latency events: a 502 returned in 5 ms
  is "under 300 ms". During an availability incident the latency SLI can
  therefore look healthier than usual.

This is accepted rather than fixed. Adding `status_class` to the
histogram would multiply its series count by 4 (13 series per
`route`/`method` pair becomes 52) to correct a metric that is only
misleading during an incident the *availability* SLO is already paging
on. The trade is documented in `docs/review.md` section 6.

## 4. SLO targets and why 99.9%

### Observed steady state

Measured on the clean baseline (`artifacts/01-baseline/`, `nominal`
profile, 50 rps for 30 minutes) and the initial smoke run:

| Metric | Smoke (20 rps, 60 s) | **Baseline (50 rps, 30 min)** |
|---|---|---|
| Requests to `/checkout` | 1,199 | **90,000** |
| Non-2xx responses | 0 | **0** |
| Availability SLI | 1.000000 | **1.000000** |
| Requests under 300 ms | 1,199 / 1,199 (100%) | **89,721 / 90,000 (99.690%)** |
| Latency SLI | 1.000000 | **0.996853** |
| Latency burn rate at steady state | 0× | **3.15×** |
| p50 / p95 / p99 | 34 / 91 / 98 ms | **34 / 94 / 159 ms** |

**Availability** consumes no budget at steady state: 90,000 requests, zero
failures, SLI exactly 1.000000.

**Latency does not.** 279 of 90,000 requests exceeded 300 ms with no fault
injected at all — 0.310%, which is **3.15× the 0.1% budget**. At that rate a
30-day latency budget is exhausted in about 9.5 days by normal operation.
The breaches are bounded (81 over 500 ms, 3 over 800 ms, none over 1 s) and
arrive in periodic bursts rather than uniformly: 87 slow requests in one
minute, then near-zero for several minutes, repeatedly through the run.
The shape is consistent with recurring sub-second stalls from the
per-request `httpx.AsyncClient` construction at `checkout/main.py:131`.

**The 60-second smoke run could not have revealed this.** At 20 rps for
60 s, 1,199 requests against a 0.31% breach rate predicts ~4 slow requests
— indistinguishable from zero on a single sample. A baseline must be long
enough and loaded enough to resolve the tail it is meant to characterise,
or it will certify a defect as clean. This is the reason the baseline table
above reports two columns rather than one.

### Consequence for the target

Observed steady state does **not** support 99.9% on latency. Two coherent
responses exist: relax the target to ~99.5% (budget 0.5%, steady-state burn
0.63×), or keep 99.9% and treat the 0.31% breach rate as a defect to fix.

**This design keeps 99.9%.** 300 ms is a statement about what checkout's
users need, and the measured breach rate has an identified cause and a known
fix (hoist the client to module scope with a connection pool, `docs/review.md`
section 7). Relaxing the SLO to 99.5% would encode the current defect as the
permanent definition of "good" and remove the pressure to fix it — the SLO
would then be measuring the implementation rather than the requirement.

The honest consequence of that choice is recorded rather than hidden: **the
service is currently non-compliant with its own latency SLO**, burning budget
at 3.15× continuously. The margin to the 6× ticket threshold is only 1.9×, so
ordinary variance occasionally pushes a 30-minute window over it — and did
exactly that during the baseline run (`docs/review.md` section 4). That is
the SLO working as intended: it converted an invisible latency defect into a
visible, quantified budget cost.

A note on reading these numbers, because it nearly produced a wrong
target here. A 5-minute-window p99 sampled shortly after the slow-burn
run began read 160 ms, suggesting latency scaled badly with load. It does
not: measured over a window containing only steady-state traffic, p99 at
50 rps is 99.5 ms with p50 at 32 ms and `inflight_requests` sitting at 2 —
statistically indistinguishable from the 20 rps figures. The 160 ms was a
start-of-run transient (per-request `httpx.AsyncClient` construction,
`checkout/main.py:131`) diluted across an otherwise short window. Any
baseline quantile must be read from a window fully inside steady state,
or the SLO threshold ends up sized for a startup artifact. Headroom to
the 300 ms threshold is therefore ≈3×, not ≈1.9×.

### The target is an alerting decision, not a description

Because burn rate is `observed_bad / (1 - SLO)`, the target alone decides
which real-world failure produces which alert severity. The table below
evaluates every candidate target against the two failure modes the
experiments inject:

| Target | Budget | Burn @ 5% errors (outage) | Burn @ 0.7% errors (degradation) | Verdict |
|---|---|---|---|---|
| 99% | 1e-2 | 5.0 | 0.7 | **fails** — a 5% outage never reaches 14.4×, so it never pages |
| 99.5% | 5e-3 | 10.0 | 1.4 | **fails** — same, outage still silent |
| **99.9%** | **1e-3** | **50.0** | **7.0** | **passes** — outage pages, degradation tickets only |
| 99.95% | 5e-4 | 100.0 | 14.0 | passes, but 14.0 vs the 14.4 page threshold is a 3% margin |
| 99.99% | 1e-4 | 500.0 | 70.0 | **fails** — a 0.7% degradation pages at 3am |

Only 99.9% and 99.95% satisfy both constraints, and 99.95% leaves a 3%
margin between the slow degradation and the paging threshold — one
unlucky window and a minor degradation wakes someone. **99.9%** is
chosen: it puts the 0.7% degradation at 7.0×, comfortably above the 6×
ticket threshold and comfortably below the 14.4× page threshold.

The latency target is set to 99.9% as well. Uniform targets across both
SLIs of one endpoint keep the error-budget policy single-valued — one
budget, one freeze threshold, one policy — rather than forcing an
arbitrary rule for what to do when one SLO is healthy and the other is
not.

## 5. Error budget

Over a 30-day rolling window, at the sustained 50 rps the experiments
use (≈129.6M requests per 30 days):

| SLO | Budget (fraction) | Budget (events) | Budget (as downtime) |
|---|---|---|---|
| Availability 99.9% | 0.001 | ≈129,600 failed requests | 43 m 12 s of total outage |
| Latency 99.9% | 0.001 | ≈129,600 requests over 300 ms | 43 m 12 s of total slowness |

Burn rate is defined as `(1 - SLI) / (1 - SLO)`; a burn rate of `B`
exhausts the 30-day budget in `30d / B`. The two alert thresholds follow
from that identity and are derived in `docs/review.md` section 2.

The two budgets are tracked separately and are **not** additive: an
endpoint that is 99.9% available and 99.9% fast is not 99.8% "good". A
single request can be both slow and failed, and the SLIs have different
denominators (the latency denominator includes 4xx, the availability one
does not).

## 6. Rejected alternatives

### 6.1 Measuring client-side or at the load balancer — rejected

The load generator already exposes `loadgen_requests_total` and
`loadgen_request_duration_seconds`, so a client-side SLI was available in
principle. Rejected for three reasons:

- **It is not actually client-side.** `loadgen` runs as a container on
  the same Docker bridge network as `checkout`. It measures neither DNS
  resolution, TLS handshake, nor any WAN path. It is a second
  *server-side* vantage point wearing a client costume — it would add
  measurement error without adding realism.
- **The denominator stops being ours.** An edge/LB SLI counts bot
  traffic, scanners, and requests rejected before they reach the service.
  Each needs an exclusion rule maintained by a team that does not own the
  edge, and every one of those rules is a place for the SLI to drift away
  from what users experience.
- **It was not even collectable.** The provided harness runs the load
  generator via `docker compose run`, which joins the network *without*
  the service alias, so `loadgen:9999` does not resolve and Prometheus
  held that target DOWN for the entire experiment. Fixed in
  `scripts/run-experiment.sh` with `--use-aliases`, but it illustrates
  the point: an SLI whose collection path is incidental is an SLI that
  will be silently missing when it matters.

In production the right answer is *both* — a server-side SLI owned by the
service team for budget accounting, and RUM for the user-experience
question. What is rejected is making the client-side signal the SLO.

### 6.2 A window-based SLI — rejected

The alternative form is "percentage of 1-minute windows in which the
error rate stayed under 1%". Rejected because it weights every window
equally regardless of traffic: a 3 a.m. window with 2 requests and 1
error counts exactly as much as a peak window with 3,000 requests and 30
errors. The event-based ratio weights by requests, which is what the
error budget is denominated in and what users actually experience. The
burn-rate identity also assumes an event ratio — window-based SLIs have
no equivalent of "spending budget 14.4× too fast".

### 6.3 One composite SLI covering both availability and latency — rejected

Combining into a single "good = 2xx AND under 300 ms" ratio was
considered. Rejected because it destroys the diagnostic signal at exactly
the moment it is needed: a composite alert cannot tell the responder
whether to open the status-class panel or the latency panel. The
`latency-burn` experiment makes this concrete — it drives the latency SLI
to zero while the availability SLI stays at 1.000, and a composite SLI
would have reported a single degraded number with no indication which
half moved. It also prevents differential routing (a latency regression
and an outage are not equally urgent) and makes the error budget
ambiguous when a single request is both slow and failed.

### 6.4 Percentile-based latency alerting — rejected

Covered in section 3: no burn rate is derivable, quantiles do not
aggregate, `histogram_quantile` interpolates, and tail-heavy tenants
distort it. Retained only as a *dashboard* panel
(`grafana/dashboards/red-overview.json`), where a human reads it for
shape rather than a rule comparing it to a threshold.

### 6.5 Counting 4xx as bad events — rejected

Covered in section 2: it would make the budget a function of customer
card quality. The rejected variant of *including* 4xx in the denominator
as good events is also rejected, because sufficient 4xx volume would
dilute the ratio and mask real 5xx.

## 7. Sources

- Baseline data: `artifacts/01-baseline/` (snapshots and range series).
- Experiment data: `artifacts/02-experiments/`.
- Recording and alerting rules: `prometheus/rules/slo.yml`.
- Alert routing and inhibition: `alertmanager/alertmanager.yml`.
- Burn-rate derivation, TTD/TTR, error-budget policy, cardinality audit:
  `docs/review.md`.
