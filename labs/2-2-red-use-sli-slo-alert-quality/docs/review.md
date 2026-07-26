# Architecture review — `POST /checkout` SLO and multi-burn-rate alerts

> **Deliverable** — homework deliverable **id=433**, "Architecture review
> and error-budget policy".

Two SLIs, one availability and one latency, both at 99.9% over 30 rolling
days, both alerted with a multi-window multi-burn-rate pair (14.4× page /
6× ticket). Four experiments were run against the live stack. The design
is in `docs/slo.md`; the rules are in `prometheus/rules/slo.yml`; the
evidence is in `artifacts/`.

## 1. SLI design choices and rejected alternatives

Both SLIs are event ratios of the form `good_events / valid_events`
measured server-side in the checkout RED middleware. The full argument is
in `docs/slo.md` sections 2, 3 and 6; what follows is the summary and the
measured facts behind each rejection.

**Availability** = `2xx+3xx / 2xx+3xx+5xx`. `4xx` is excluded from *both*
sides. A 4xx on this endpoint is the payments stub declining a charge and
checkout forwarding that status (`checkout/main.py:153-157`) — the service
did its job and the answer was "no". Counting declines as violations makes
the error budget a function of customer card quality, and would eventually
page an engineer who cannot act. Excluding rather than counting-as-good is
deliberate: a decline is not evidence of health in either direction, so it
should not dilute the ratio. Residual risk, recorded because it is
invisible otherwise: `429`/`408` fall into the same `4xx` class and *are*
server-side failures. The service emits neither today, so the exclusion is
currently exact; adding rate limiting invalidates it.

**Latency** = `bucket{le="0.3"} / count`, threshold-style at 300 ms.
`checkout/main.py:38` places an exact bucket boundary at `0.3`, so the
numerator is a true count, not an interpolation.

Rejections, each with the reason that actually decided it:

| Alternative | Rejected because |
|---|---|
| Percentile-style latency alert (`histogram_quantile(0.99) > 0.3`) | No burn rate is derivable from a quantile — "p99 = 350 ms" does not express a fraction of budget spent, so the entire multi-window apparatus is unavailable. Quantiles also do not aggregate across windows or instances, and `histogram_quantile` interpolates *within* the bucket, making the value partly an artifact of bucket layout. |
| Client-side / load-balancer SLI | The available client vantage point (`loadgen_request_duration_seconds`) runs as a container on the same Docker bridge as checkout — no DNS, no TLS, no WAN. It is a second server-side measurement wearing a client costume. It was also **not collectable**: `docker compose run` joins the network without the service alias, so `loadgen:9999` never resolved and Prometheus held that target DOWN for the entire first experiment. Fixed with `--use-aliases` in `scripts/run-experiment.sh`. |
| Window-based SLI ("% of minutes under 1% errors") | Weights a 3 a.m. window with 2 requests equally with a peak window with 3,000. The budget is denominated in events, so the SLI must be too. |
| Single composite SLI (good = 2xx **and** < 300 ms) | Destroys the diagnostic signal exactly when it is needed. The `latency-burn` experiment isolates the two SLIs by construction — 400 ms injected latency with zero 5xx (`CLAIM_SLI_INDEPENDENCE`). A composite would have reported one degraded number with no indication which half moved, and would have prevented differential routing (page vs ticket). |
| `status_class` on the latency histogram | Would let the latency SLI exclude 4xx and stop counting fast failures as good, but multiplies histogram series 4× (14 → 56 per route/method) to correct a metric that only misleads during an incident the availability SLO already pages on. See section 6. |

## 2. Burn-rate math: why 14.4× and 6×

Burn rate is defined as

```
burn = (1 - SLI) / (1 - SLO) = observed bad fraction / budgeted bad fraction
```

At 99.9%, the budget is `1 - 0.999 = 0.001`, i.e. 0.1% of valid events, or
equivalently **43 m 12 s** of total unavailability per 30 days. Burn rate
1.0 means budget is being consumed exactly as fast as it accrues: the
30-day budget lasts exactly 30 days. A burn rate of `B` exhausts it in
`30d / B`.

**The page threshold, 14.4×:**

```
30 d / 14.4      = 50 hours to exhaustion
1 h at 14.4×     = 1/50 of the budget = 2% of a 30-day budget in one hour
```

Spending 2% of a month's budget in a single hour is the definition of
something worth waking a human for. The 14.4 is not arbitrary — it is
`0.02 × 720 h`, chosen so the alerting window (1 h) and the budget
fraction (2%) line up.

**The ticket threshold, 6×:**

```
30 d / 6         = 5 days to exhaustion
6 h at 6×        = 6/120 of the budget = 5% of a 30-day budget in six hours
```

5% over six hours is real budget leakage but not an emergency: it is
work for business hours, hence `severity: ticket` and a routing path that
does not page (`alertmanager/alertmanager.yml`).

### Why the AND of two windows is non-negotiable

The long window establishes that enough budget has actually been spent to
justify the alert; the short window establishes that it is *still* being
spent. Dropping either produces a specific, well-understood failure — and
both were observed in this lab rather than merely asserted:

- **Drop the short window** and the alert keeps firing long after
  recovery, because `rate()` over 6 h decays slowly.
  `CLAIM_LONG_WINDOW_DECAY`
- **Drop the long window** and a 30-second blip pages. At 50 rps the 5 m
  window holds ~15,000 requests, so a burst of a few hundred errors — one
  bad deploy that is rolled back in a minute — crosses 14.4× on the short
  window alone.

The same asymmetry should show up as a *cross-experiment* protection:
during the `latency-burn` run the 1 h availability window still contains
the previous fast-burn experiment's 5% errors, while the 5 m availability
window is clean. `CLAIM_CROSS_EXPERIMENT`

`for: 2m` on all four alerts: at a 15 s evaluation interval that requires
8 consecutive true evaluations. The short window already smooths
single-scrape noise, so a longer `for` would mostly add detection latency
rather than suppress anything real.

## 3. Experiment results

Four experiments, each with before/after snapshots and a padded range
capture in `artifacts/`. TTD and TTR are read from the `ALERTS` metric,
which exists only while an alert is pending or firing — so the first and
last timestamps of the `alertstate="firing"` series are the detect and
reset moments, measured rather than estimated.

| # | Experiment | Profile | Duration | Injected | Fast fired? | Slow fired? | TTD | TTR |
|---|---|---|---|---|---|---|---|---|
| 1 | Slow-burn degradation | `slow-burn` | 90 min | 0.7% errors | no (7.19× < 14.4×) | **yes** | 456 s (7.6 min) | `TTR_SLOW` |
| 2 | Steady-state baseline | `nominal` | 30 min | none | no | no | n/a | n/a |
| 3 | Fast-burn outage | `fast-burn` | 12 min | 5% errors | **yes** | `SLOW_DURING_FAST` | `TTD_FAST` | `TTR_FAST` |
| 4 | Latency degradation | `latency-burn` | 15 min | 400 ms ±50 ms | **yes (latency)** | `SLOW_DURING_LAT` | `TTD_LAT` | `TTR_LAT` |

Observed burn rates against prediction:

| Experiment | Predicted burn | Measured burn | Window |
|---|---|---|---|
| `slow-burn` | 7.0× | 7.19× | 6 h availability |
| `fast-burn` | 50× | `MEASURED_FAST` | 1 h availability |
| `latency-burn` | ≈1000× | `MEASURED_LAT` | 1 h latency |
| `nominal` | 0× | `MEASURED_NOMINAL` | all |

### Experiment order is part of the method, not an afterthought

The single most consequential finding of this lab is that **the same
correct alert fires or does not fire depending on what ran before it**,
because `rate()` over a long window mixes the current fault with whatever
traffic preceded it.

Concretely, the slow-burn alert needs `6h ≥ 6×`. Running it first, with
only the 60 s smoke run in the 6 h window, the ratio reads a true 0.7% →
burn 7.19×, and it fires. Had the 30-minute nominal baseline run first,
its 90,000 clean requests would have diluted the same window:

```
bad/valid = (0.007 × 50 × 5400) / (50 × 5400 + 50 × 1800)
          = 1890 / 360000 = 0.525%   →  burn 5.25×  →  never fires
```

A correctly implemented alert would have looked broken, and the obvious
but wrong conclusion would have been to lower the threshold. Similarly,
the fast-burn alert needs a quiet hour ahead of it: run straight after the
baseline, its 1 h window requires `t ≥ 12.1 min` of injection to cross
14.4×, and the injection is only 12 min — it would have missed its own
outage, then fired afterwards as clean samples aged out, reporting a
badly inflated TTD.

The experiments were therefore ordered `slow-burn → nominal → (1 h gap) →
fast-burn → latency-burn`, and the spacing is recorded in each
`*.meta.json`. **Any TTD measured against a multi-window alert is a
property of the window contents, not only of the fault.** In production
the same effect means a recovering service can page for an incident that
ended, and a fault beginning right after a quiet period is detected faster
than the identical fault beginning during peak traffic.

## 4. False-positive analysis

No alert fired during the `nominal` baseline. That is a structural
property, not luck, and it has three separate causes worth naming:

1. **The dual-window AND.** The 6 h availability window still carried
   residue from the preceding slow-burn experiment, yet the 30 m window
   read 0 during the clean baseline, so `SLOSlowBurnAvailability` could
   not fire — the "is it still happening" test doing its job on real
   data. `CLAIM_BASELINE_RESIDUE`
2. **`for: 2m`.** Eight consecutive true evaluations are required, so a
   single bad scrape or one-off glitch cannot fire anything.
3. **Volume.** At 50 rps the 5 m window holds ~15,000 requests. Crossing
   14.4× requires a bad fraction of 1.44%, i.e. **≈216 errors within 5
   minutes**. No plausible transient produces that.

### The real false-positive risk is low traffic, not noise

This alert geometry is safe at 50 rps and progressively unsafe as traffic
falls, because the 5 m window's resolution is `1/(300 × rps)`:

| Offered load | Requests in 5 m | Errors needed to hit 14.4× | Verdict |
|---|---|---|---|
| 50 rps | 15,000 | 216 | robust |
| 10 rps | 3,000 | 44 | acceptable |
| 1 rps | 300 | 5 | fragile |
| 0.2 rps | 60 | 1 | **a single error pages** |

At 0.2 rps one failed request is 1.67% of the window — burn 16.7×, above
the page threshold. Overnight or on a low-traffic tenant this geometry
would page on statistical noise. Two mitigations, neither yet applied
because the lab runs at a constant 50 rps:

- Add a minimum-volume guard to the alert expression, e.g.
  `AND sum(rate(http_requests_total{...}[5m])) > 1`, so the alert simply
  cannot fire when there is not enough traffic to measure.
- Or widen the short window at low traffic (5 m → 30 m), trading
  detection latency for resolution.

There is also a **failure mode these alerts cannot detect at all**: if the
service stops serving entirely, both SLI ratios return *no data* rather
than 0, every comparison becomes false, and all four alerts go silent.
This is correct in the lab (no requests means no user harm, and `loadgen`
genuinely does stop between runs) but wrong in production. It needs a
separate liveness alert on `up{job="checkout"} == 0` or on
`absent(http_requests_total{...})`, which is out of scope here but is a
real gap in the current config.

## 5. Error-budget policy

This is the policy I would attach to `checkout` in production.

**Budget.** 0.1% of valid events per 30 rolling days, per SLO, tracked
separately for availability and latency: ≈129,600 events each at 50 rps,
equivalently 43 m 12 s of total outage. The two budgets are not additive —
a request can be both slow and failed, and the SLIs have different
denominators.

**Freeze threshold.** When remaining budget for either SLO drops below
**25%** (≈32,400 events / ~11 minutes), the service enters a change
freeze: no non-essential deploys to `checkout` or to `payments` on the
checkout path. Reliability work, rollbacks, and fixes that demonstrably
reduce burn are explicitly *not* frozen — the freeze exists to stop
budget consumption, not to stop repair.

**Always exempt from the freeze**, no approval needed: security patches
with a CVE of High or above; rollbacks; config changes that reduce load or
error rate; anything required by a regulatory deadline with the deadline
documented in the change record.

**Override approver.** The service owner (checkout tech lead) plus the
on-call SRE for the current rotation, jointly. Either alone is not
sufficient — the point of two signatures is that one holds delivery
pressure and the other holds reliability pressure. Evidence required
before an override is granted:

1. The burn-rate panel showing current 1 h and 6 h burn rates.
2. A written statement of why the change cannot wait for budget recovery.
3. A named rollback trigger — a specific metric and threshold at which the
   change is reverted without further discussion.
4. Confirmation that the change is behind a flag or is otherwise
   revertible within one deploy cycle.

**Exit criteria.** The freeze lifts when remaining budget is **≥50%** and
has been non-decreasing for **7 consecutive days**. Both conditions
matter: 50% alone can be met by the 30-day window simply rolling past an
old incident, which is budget recovery on paper rather than a fixed
service. The 7-day trend is what distinguishes the two.

**Special cases.** Launch weeks: budget may be pre-spent by agreement,
with a written expectation of the burn rate and an explicit end date —
but the freeze threshold is not waived, it is *pre-approved*, and the
alerts stay on. Incidents caused by a dependency outside the team's
control still consume budget (the user experienced it), but they do not
count toward the freeze if a dependency-attribution note is filed within
48 hours; this keeps the policy honest without punishing teams for other
people's outages.

**Review cadence.** Budget consumption is reviewed weekly. Two
consecutive months of consuming under 20% of budget is a signal the SLO
is too loose and should be tightened — an SLO that is never at risk is
not measuring anything.

## 6. Cardinality audit

Measured from `/api/v1/status/tsdb` and
`count by (__name__)({__name__=~"http_.*"})`; raw data in
`artifacts/cardinality.json`.

Total Prometheus head series across the whole stack: **1,049**. Of that,
the metrics backing both SLIs account for **19 series**.

| Metric | Labels shipped | Distinct values observed | Bound on values | Series | Decision |
|---|---|---|---|---|---|
| `http_requests_total` | `route` | 1 (`/checkout`) | router templates only, never instantiated paths (`checkout/main.py:100-103`) | 2 | keep |
| | `method` | 1 (`POST`) | HTTP verb set, ~7 | | keep |
| | `status_class` | 2 (`2xx`,`5xx`) | hard-coded 4-value enum (`checkout/main.py:61-70`) | | keep |
| `http_request_duration_seconds_bucket` | `route`, `method`, `le` | 1 × 1 × 12 | 11 fixed buckets + `+Inf` | 12 | keep |
| `http_request_duration_seconds_count` / `_sum` | `route`, `method` | 1 × 1 | as above | 2 | keep |
| `inflight_requests` | none | — | single gauge | 1 | keep |
| `http_requests_created`, `http_request_duration_seconds_created` | `route`,`method`(,`status_class`) | — | `prometheus_client` artifact | 3 | keep (see note) |

**Worst-case bound.** Even with every route instrumented and every verb
in use, the RED metrics are bounded at `routes × 7 × 4` for the counter
and `routes × 7 × 13` for the histogram. For a service with 20 endpoints
that is 560 + 1,820 ≈ 2,400 series — still trivial. The design is
cardinality-safe *by construction*, because every label has a bound that
comes from the code or the protocol, not from user input.

### Labels considered and rejected

| Rejected label | Cardinality | Why rejected |
|---|---|---|
| `user_id` | unbounded (one per customer) | Multiplies every series by the user count. A 100k-user service turns 19 series into 1.9M. Belongs in logs/traces where storage is per-event, not per-series. |
| `request_id` / `trace_id` | unbounded (one per request) | Pathological: series count grows linearly with traffic forever, and each series holds exactly one sample. This is the canonical metrics anti-pattern — it is a log line with extra steps. |
| raw URL path | unbounded (path parameters) | `/checkout/12345` and `/checkout/12346` become distinct series. The middleware deliberately uses the route template instead (`checkout/main.py:100-103`). |
| `payments_upstream_status` | ~10 | Tempting for debugging *why* checkout 5xx'd, and cheap. Rejected because it belongs on a separate dependency-health metric, not on the SLI metric: any label on the SLI metric is a label the SLI query must aggregate away, and a forgotten `sum()` silently changes the SLI. |
| `status_class` on the latency histogram | ×4 on 14 series | Would fix the "fast failures count as good latency" caveat, but 4× the histogram cost to correct a metric that only misleads during an incident the availability SLO already pages on. Revisit if the two SLOs ever get different owners. |
| `instance` (kept implicitly) | 1 per replica | Not rejected but noted: `sum()` in every SLI rule aggregates it away, which is required — a per-instance SLI would let one bad replica hide behind healthy ones, or vice versa. |

**One piece of avoidable waste**: `prometheus_client` emits `*_created`
gauges (3 series) that carry metric creation timestamps and are never
queried. They can be suppressed but are left in place, since 3 series
against 1,049 is not worth a code change.

Perspective worth keeping: **Prometheus's own self-monitoring is ~50×
larger than the entire SLO pipeline** — `prometheus_http_request_duration_seconds_bucket`
alone is 130 series, and `__name__` has 329 distinct values. The cost of
bounded-cardinality SLI instrumentation is negligible; the cost of
*unbounded* instrumentation is unbounded. That asymmetry is the whole
argument.

## 7. 10× growth analysis: 50 → 500 rps

Measured at 50 rps: `checkout` at **55% of one CPU**, `payments` at 11.8%,
`inflight_requests` = 2, p50 32 ms, p99 99.5 ms, Prometheus at 1,049 head
series and 65 samples/s.

| Component | What saturates | Measured evidence | Design change |
|---|---|---|---|
| **checkout event loop (first to break)** | Single `uvicorn` worker is one process on one event loop. 55% of a core at 50 rps extrapolates to 100% at **≈90 rps** — saturation arrives at under 2×, long before 10×. Past that, latency climbs and the 300 ms SLO breaks even with zero errors. | `docker stats`: 55.05% CPU; `inflight_requests`=2 confirms no queuing yet, so the wall is CPU, not concurrency | Run multiple workers (`--workers`) and scale horizontally behind a load balancer; add a per-route SLO so a slow endpoint cannot consume the whole budget |
| **Outbound socket churn to payments** | `checkout/main.py:131` constructs a new `httpx.AsyncClient` **inside every request**, so every checkout opens and closes its own TCP connection. At 500 rps that is 500 connections/s and a growing pile of `TIME_WAIT` sockets — ephemeral port exhaustion, not CPU, becomes the ceiling | per-request `async with httpx.AsyncClient(...)` in the handler; also the likely cause of the 160 ms start-of-run p99 transient | Hoist to a module-level client with an explicit connection pool and keep-alive. This is the single highest-value fix in the service |
| **payments stub** | 11.8% of a core at 50 rps, mostly `asyncio.sleep`. Scales to ~118% at 500 rps — saturates a single core at roughly 8× | `docker stats` | Same treatment; in reality this is a stand-in for a real dependency with its own SLO |
| **Prometheus ingestion** | **Does not grow with traffic.** Series count is bounded by label design and sample rate is set by `scrape_interval`, not request volume. 500 rps produces exactly the same 19 SLI series and the same 65 samples/s as 50 rps | `artifacts/cardinality.json`; `rate(prometheus_tsdb_head_samples_appended_total[2m])` = 65.4 | No change. This is the payoff of bounded cardinality: the observability bill is decoupled from the traffic bill |
| **Alertmanager fan-out** | Driven by alert count (4 rules, ≤4 notifications per group interval), not by request rate | `alertmanager/alertmanager.yml` grouping on `[alertname, service, slo]` | No change at 10×. Would matter only with per-tenant or per-route alert explosion |
| **Grafana burn-rate panels** | 6 h `rate()` over a fixed sample count, unchanged by traffic | as above | No change; if panels do get slow, query the recording rules (already done) rather than raw histograms |

**Two non-obvious consequences of 10× growth:**

- **The alerts get *better*, not worse.** Thresholds are ratios, so they
  need no retuning. Meanwhile the 5 m window's resolution improves from
  15,000 to 150,000 requests, so the low-traffic false-positive risk in
  section 4 disappears entirely. Growth moves this design away from its
  weak regime.
- **The budget grows in absolute terms**: 0.1% of 1.296 B requests is
  ≈1.296 M failed requests per 30 days. The *time*-equivalent (43 m 12 s)
  is unchanged, which is exactly why time-based budget statements are more
  stable to reason about across growth than event counts.

## 8. What I would do differently next iteration

Three things. First, I would add the minimum-volume guard from section 4
before shipping, rather than documenting it as a known gap — the current
geometry is only safe at the load this lab happens to run at, and "safe at
current traffic" is not a property that survives contact with a traffic
dip. Second, I would add the liveness alert (`up == 0` /
`absent(http_requests_total)`) in the same change, because an SLO pipeline
that goes silent when the service dies is worse than no pipeline: it
actively signals health. Third, and most importantly, I under-estimated
how much experiment *ordering* mattered — I derived the window-dilution
effect only after computing why a correct alert would have failed to fire
had I run the baseline first. Next time I would model the window contents
for the whole planned sequence before running anything, because the cost
of discovering it late is a 90-minute experiment that proves nothing.

Two smaller notes: the 160 ms p99 that turned out to be a start-of-run
transient nearly became the number I sized a threshold against, which
argues for always reading baselines from a window fully inside steady
state; and the `loadgen` scrape target sat DOWN through an entire
experiment without anything complaining, which argues for a
`up{job=~".*"} == 0` alert on the monitoring stack itself, not just on the
service under test.
