# Architecture review — `POST /checkout` SLO and multi-burn-rate alerts

> **Deliverable** — homework deliverable **id=433**, "Architecture review
> and error-budget policy".

Two SLIs, one availability and one latency, both at 99.9% over 30 rolling
days, both alerted with a multi-window multi-burn-rate pair (14.4× page /
6× ticket). Four experiments were run against the live stack. The design
is in `docs/slo.md`; the rules are in `prometheus/rules/slo.yml`; the
evidence is in `artifacts/`.

| Where to find each topic | Section |
|---|---|
| The two SLI definitions and rejected alternatives | 1 |
| Burn-rate math, and why 14.4×/6× are right *for this service* | 2 |
| Measured time-to-detect and time-to-reset per experiment | 3 |
| False positives, false negatives, and the tuning that followed | 4 |
| Error-budget policy: freeze level and override sign-off | 5 |
| Label cardinality audit | 6 |
| What breaks first at 10× traffic | 7 |

**Three results worth reading even if nothing else is:**

1. The zero-fault baseline fired an alert, and the alert was **right**. The
   service breaches its own 300 ms latency SLO on 0.31% of requests with
   nothing injected — a 3.15× steady-state burn. The 60-second smoke run
   used to set the original target lacked the resolution to see it
   (section 4).
2. **Time-to-reset is governed by the short window's length**, not by how
   fast the service recovers. One fault, one recovery, two alerts clearing
   25 minutes apart: 315 s on a 5 m window, 1815 s on a 30 m window
   (section 2).
3. During `latency-burn` the 6 h availability burn read **41.68×** for an
   outage that had ended half an hour earlier, while every shorter window
   read 0. The dual-window AND is the only reason nothing paged
   (section 2).

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
| Single composite SLI (good = 2xx **and** < 300 ms) | Destroys the diagnostic signal exactly when it is needed. The `latency-burn` experiment isolates the two SLIs by construction — 400 ms injected latency with zero 5xx. Measured mid-run: availability SLI **1.000000** while the latency SLI read **0.000070** — total separation, and no availability alert fired. A composite would have reported one degraded number with no indication which half moved, and would have prevented differential routing (page vs ticket). |
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

### Are 14.4× and 6× right *for this service*?

The derivation above is generic — it follows from a 30-day budget and
nothing else. Whether those numbers are correct **here** depends on where
this service's actual failure modes land relative to them. Measured:

| Condition | Measured burn | 14.4× page | 6× ticket | Correct outcome? |
|---|---|---|---|---|
| Availability, steady state | 0× (SLI 1.000000) | no | no | yes — silent |
| Availability, 0.7% degradation | **7.19×** | no | **yes** | yes — ticket, not a page |
| Availability, 5% outage | **48.9×** | **yes** | yes | yes — pages |
| Latency, 100% breach | **≈1000×** | **yes** | yes | yes — pages |
| **Latency, steady state** | **3.15×** | no | no | **only just** |

For **availability** the thresholds are well placed. The two injected
failure modes land at 7.19× and 48.9×, either side of 14.4× with wide
margins — a factor of 2 below and a factor of 3.4 above. Nothing this
service does at steady state comes near either threshold, because
steady-state availability burn is exactly zero across 90,000 requests. The
separation is not a lucky coincidence: it follows from choosing the SLO
target so that it separates the regimes (`docs/slo.md` section 4), and the
thresholds and the target are two halves of one decision.

For **latency** they are not comfortable, and the honest answer is that
the 6× ticket threshold is currently too tight for this service. Steady
state burns 3.15×, so the margin to a ticket is **1.9×**, not the 6× of
headroom the number implies. Ordinary variance crosses it — that is
exactly what happened during the baseline run (section 4). Three ways to
respond, in order of preference:

1. **Fix the service.** 3.15× steady-state burn is a defect with a known
   cause (`checkout/main.py:131`). Removing it restores the margin to the
   ~1000× the thresholds were designed around. This is the right answer
   and the reason the target was not relaxed.
2. **Widen the latency slow-burn short window** from 30 m to 1 h. The
   baseline violation was a 90-second burst; a longer short window
   averages it away without touching the threshold, at the cost of
   detection latency on real slow burns.
3. **Raise the latency ticket threshold** from 6× to, say, 10×. Cheapest
   and worst: it silences the alert without addressing why it fires, and
   it would also silence a genuine 1% latency regression.

None of the three is applied here — the thresholds are left at the
standard 14.4×/6× so the experiment results are comparable to the
canonical geometry, and the mismatch is documented rather than tuned
away. **A threshold that is right for one SLO of a service is not
automatically right for another SLO of the same service**, because it is
the distance between steady state and the threshold that matters, not the
threshold alone.

### Why the AND of two windows is non-negotiable

The long window establishes that enough budget has actually been spent to
justify the alert; the short window establishes that it is *still* being
spent. Dropping either produces a specific, well-understood failure — and
both were observed in this lab rather than merely asserted:

- **Drop the short window** and the alert keeps firing long after
  recovery, because `rate()` over a long window decays slowly. Measured on
  the `fast-burn` run: one fault, one moment of recovery, two alerts that
  cleared **25 minutes apart** — `SLOFastBurnAvailability` at 315 s,
  `SLOSlowBurnAvailability` at 1815 s. The `latency-burn` run reproduces
  it independently: 285 s and 1785 s.

  **TTR is governed by the short window's length, not by `for` and not by
  how fast the service recovered.** 315 s and 285 s against a 5 m window;
  1815 s and 1785 s against a 30 m window — in every case within a scrape
  interval or two of the window length itself. `for` delays *firing*; it
  plays no part in clearing. The practical consequence: an operator
  watching the 6 h burn rate after a fix is watching a number that
  physically cannot recover for half an hour, and will conclude the fix
  did not work.
- **Drop the long window** and a 30-second blip pages. At 50 rps the 5 m
  window holds ~15,000 requests, so a burst of a few hundred errors — one
  bad deploy that is rolled back in a minute — crosses 14.4× on the short
  window alone.

The same asymmetry showed up as *cross-experiment* protection, and it is
the cleanest demonstration in the lab. Measured during the `latency-burn`
run, the availability windows read:

```
6h  = 41.68x   <- still full of the previous experiment's 5% errors
1h  =  0.00x
30m =  0.00x
5m  =  0.00x
```

The 6 h burn rate sat at nearly 7× the ticket threshold, describing an
outage that had ended half an hour earlier. `SLOSlowBurnAvailability`
did not fire, because its 30 m partner read zero. Without the AND, this
run would have paged for the *previous* experiment — and in production,
every incident would be followed by a stream of alerts about itself for
as long as the longest window.

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

TTD and TTR are measured from the **true load window** — the first and last
increment of `http_requests_total` — not from the wrapper script's
timestamps. The two differ by up to 5.8 minutes on the slow-burn run
(see the suspension note below), so script timestamps are not a sound
reference.

| # | Experiment | Profile | Load | Injected | Alerts fired | TTD | TTR |
|---|---|---|---|---|---|---|---|
| 1 | Slow-burn degradation | `slow-burn` | 90 min | 0.7% errors | `SLOSlowBurnAvailability` only — fast correctly silent at 7.19× < 14.4× | 435 s | not measured¹ |
| 2 | Fast-burn outage | `fast-burn` | 12 min | 5% errors | `SLOFastBurnAvailability` **and** `SLOSlowBurnAvailability` (48.9× exceeds both thresholds) | 165 s both | **315 s** fast / **1815 s** slow |
| 3 | Latency degradation | `latency-burn` | 15 min | 400 ms ±50 ms | `SLOFastBurnLatency` **and** `SLOSlowBurnLatency`; **no availability alert** | 165 s both | **285 s** fast / **1785 s** slow |
| 4 | Steady-state baseline | `nominal` | 30 min | none | `SLOSlowBurnLatency`, 90 s — **a true positive, not a false one** (section 4) | 270 s | self-cleared mid-run |

¹ The slow-burn run straddled a machine suspension. macOS `time.monotonic()`
does not advance while the host sleeps, so the load generator delivered its
full 90 minutes of monotonic-time load across 95.8 minutes of wall clock,
and Prometheus lost scrapes around 00:00Z. The alert was still firing when
scraping stopped, so the apparent resolve is suspension, not recovery. The
equivalent measurement is recovered from experiments 2 and 3, which produce
the same alert pair under continuous scraping. **An experiment that sleeps
mid-run loses exactly the data it exists to produce, and the loss is
indistinguishable from a clean resolve** — every subsequent run was executed
under `caffeinate -i`.

Observed burn rates against prediction:

| Experiment | Predicted burn | Measured burn | Window |
|---|---|---|---|
| `slow-burn` | 7.0× | **7.19×** | 6 h availability |
| `fast-burn` | 50× | **48.9×** | 1 h availability |
| `latency-burn` | ≈1000× | **≈1000×** (SLI 0.000070) | 1 h latency |
| `nominal` availability | 0× | **0×** (SLI 1.000000) | all |
| `nominal` latency | 0× | **3.15×** — prediction wrong, see section 4 | 30 m latency |

Every prediction held except the last, and the one that failed is the most
informative result in the lab.

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

## 4. False positives, false negatives, and how the alerts were tuned

**An alert did fire during the `nominal` baseline, and it was correct.**

`SLOSlowBurnLatency` fired 270 s into the zero-fault baseline and cleared
90 s later. The obvious write-up is "false positive, alert too sensitive".
The measurement says otherwise: during that window the service genuinely
served ~0.9% of requests slower than 300 ms, against a 0.1% budget. The
alert reported a real SLO violation. It is a **true positive against a
target the service does not meet**.

The full baseline: 90,000 requests, availability SLI exactly 1.000000, and
latency SLI 0.996853 — **279 requests over 300 ms with nothing injected**,
a steady-state latency burn rate of **3.15×**. Breaches are bounded (81
over 500 ms, 3 over 800 ms, none over 1 s) and arrive in bursts — 87 slow
requests in a single minute, then near-zero for several, repeatedly. The
signature of recurring sub-second stalls, consistent with the per-request
`httpx.AsyncClient` construction at `checkout/main.py:131`.

Two corrections I had to make to my own analysis here, both worth
recording because each was the comfortable answer:

1. I first attributed the slow requests to a **cold start**, having seen a
   similar spike at the beginning of the `fast-burn` run. A per-minute
   breakdown killed that: the bursts recur throughout all 30 minutes
   (86.7 slow at 10:14, 86.7 at 10:22, 42.7 at 10:26, 21.3 at 10:31), and
   the first 60 s of the baseline had **zero**. Start-of-run was the
   pattern I expected, so it was the pattern I saw.
2. I was about to record the baseline as clean because **the 60-second
   smoke run showed 100% under 300 ms**. At 20 rps for 60 s, a 0.31%
   breach rate predicts ~4 slow requests out of 1,199 — comfortably zero
   on one sample. The short baseline did not show the service was healthy;
   it lacked the resolution to show it was not.

So the SLO target was originally justified against a measurement too small
and too light to see the defect it was meant to bound. The target is kept
at 99.9% deliberately (`docs/slo.md` section 4): 300 ms is what users need,
the breach has a known cause and fix, and relaxing to 99.5% would encode
the defect as the definition of good. The service is therefore **currently
non-compliant with its own latency SLO**, and the margin from 3.15× to the
6× ticket threshold is only 1.9× — narrow enough that ordinary variance
crosses it, which is precisely what happened during the baseline.

### Why the availability alerts did not false-positive

No availability alert fired during the baseline. That is structural, not
luck, and it has three separate causes worth naming:

1. **The dual-window AND.** Measured mid-baseline, the availability burn
   rates were `6h = 13.68×`, `1h = 0`, `30m = 0`, `5m = 0`. The 6 h window
   still carried the fast-burn experiment's errors and stood at more than
   twice the ticket threshold, while every shorter window read exactly
   zero. `SLOSlowBurnAvailability` could not fire because its 30 m
   partner was clean — the "is it still happening" test doing its job on
   real data. On the long window alone, a perfectly healthy 30-minute
   baseline would have ticketed.
2. **`for: 2m`.** Eight consecutive true evaluations are required, so a
   single bad scrape or one-off glitch cannot fire anything. **This was
   not hypothetical — it caught a real one.** During the `fast-burn` run
   (which injects *errors*, not latency) the latency burn rate spiked to
   **10.61×**, well above the 6× ticket threshold, and
   `SLOSlowBurnLatency` entered `pending` at 08:17:30Z. It never fired:
   the spike lasted about 60 s and decayed below 6× before the `for`
   timer elapsed.

   The cause is the same periodic stall documented above, not the
   injected fault: 22.7 requests over 300 ms in the first 60 s against
   17.4 across the remaining 11 minutes. Two effects compound. The burst
   is intrinsic to the service and would have happened anyway, and it
   landed while the 5 m window held only ~1 minute of traffic — so a few
   dozen slow requests were over 1% of a small denominator, ten times the
   budget.

   (I initially read this as a cold-start effect specific to run start.
   The baseline later disproved that: its own first 60 s contained zero
   slow requests, and the bursts recur throughout. The spike here is a
   burst that happened to land early, amplified by a nearly empty
   window.)

   The lesson is about window occupancy, not traffic rate: **an alert
   window that has just begun filling has almost no noise immunity**,
   because a handful of events is a large fraction of a small
   denominator. This is the same failure mode as the low-traffic column
   in the table below, reached from a different direction — a window
   recovering from a traffic gap is statistically identical to a
   low-traffic window. `for: 2m` is what stopped it from becoming a
   ticket here, and it is doing load-bearing work rather than decoration.
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

### False negatives

False positives are visible — someone gets woken and complains. False
negatives are silent, so they have to be looked for deliberately. Four
were found in this config, in descending order of severity.

**1. Total outage produces total silence.** If the service stops serving
entirely, both SLI ratios evaluate `0/0` and return *no data*, not 0.
Every comparison against a no-data vector is false, so all four alerts go
quiet. **The pipeline is indistinguishable from perfect health when the
service is gone** — the worst possible failure mode for an alert, and the
one an SLO-only alerting strategy is structurally prone to. Verified
directly: with no traffic, `slo:checkout_availability:burnrate6h` returns
`NaN` and nothing fires. This is benign in the lab (`loadgen` genuinely
stops between runs, and no requests means no user harm) and unacceptable
in production.

**2. A sustained 1% error rate never pages.** At a 99.9% target, 1%
errors is burn 10× — above the 6× ticket threshold, below the 14.4× page
threshold. So a service failing one request in a hundred, indefinitely,
generates a ticket and never a page, while exhausting a 30-day budget in
three days. This is the intended behaviour of the geometry rather than a
defect, but it is worth stating plainly: **the fast-burn alert is a
detector of severe short outages, not of moderate persistent badness.**
The slow-burn alert and the error-budget policy are what catch the
latter, which is why the policy in section 5 has teeth independent of
paging.

**3. Window dilution can silence a correct alert.** Documented in
section 3: the same 0.7% degradation fires at 7.19× with an empty 6h
window and 5.25× — silent — if the window is half full of clean traffic.
In production this means a fault beginning right after a traffic ramp is
detected later than the identical fault beginning after a quiet period,
and a short incident inside a busy window can be averaged below threshold
entirely.

**4. The latency SLI counts fast failures as good.** The histogram has no
`status_class` label, so a 502 returned in 5 ms is a "good" latency
event. During an availability incident the latency SLI therefore looks
*better* than usual. It cannot mask the incident — availability alerts
fire on the same traffic — but any attempt to read the latency SLO during
an outage will mislead.

### How the alerts were tuned, and what was deliberately left alone

Tuning decisions actually made:

| Decision | Rationale | Evidence it was needed |
|---|---|---|
| Dual-window AND on all four alerts | long window proves the budget was really spent, short window proves it still is | 6h read 41.68× during `latency-burn` for an outage that had ended; only the AND suppressed it |
| `for: 2m` on all four | requires 8 consecutive true evaluations at a 15 s interval | caught a real 10.61× latency spike during `fast-burn` that would otherwise have ticketed |
| Short window kept at 5 m / 30 m rather than lengthened | TTR is bounded by the short window (315 s vs 1815 s measured), so lengthening it directly delays recovery signalling | measured in section 2 |
| SLO target chosen to separate the failure regimes | thresholds and target are one decision, not two | 7.19× vs 48.9× land either side of 14.4× |

Deliberately **not** applied, with reasons — each is a known gap rather
than an oversight:

- **Minimum-volume guard.** Would fix false positive #1 (low traffic) and
  the window-occupancy variant. Not added because this lab runs at a
  constant 50 rps where it would never engage, so it would ship untested —
  and an untested alert clause is itself a risk. It is the first thing to
  add before this config sees variable traffic. Note the guard must bound
  *events in the window*, not the request rate: the baseline true positive
  happened at a full 50 rps with a window that had only just started
  filling, so a rate-based guard would not have caught it.
- **Liveness alert.** Fixes false negative #1 and is genuinely out of
  scope for an SLO-focused lab, but is the single most important addition
  for production use.
- **Latency threshold or window changes.** Discussed in section 2; the
  right fix is the service defect, not the alert.

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

Total Prometheus head series across the whole stack, after all four
experiments: **1,073**. Of that, the metrics backing both SLIs account for
**19 series** (`http_*`), or 20 counting the in-flight gauge — **1.9% of
the TSDB**. Four experiments including a 90-minute run added no series at
all beyond the `5xx` status class appearing for the first time: series
count is a function of label design, not of traffic or time.

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
