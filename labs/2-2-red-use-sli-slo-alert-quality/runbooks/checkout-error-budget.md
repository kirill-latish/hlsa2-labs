# Runbook — checkout SLO error-budget burn

This runbook is linked from the Prometheus alert annotations
(`runbook_url` field in `prometheus/rules/slo.yml`). It is written to be
useful at 3am, not at training time.

## Pager: `SLOFastBurnAvailability`

**Why you were paged.** The 1h burn rate AND the 5m burn rate are both
≥ 14.4× the SLO budget. At this rate, the 30-day budget will be
exhausted in under ~2 days. Real users are seeing 5xx now.

### Triage in two minutes

1. Open the burn-rate panel:
   `http://localhost:3000/d/red-overview/red-overview?var-service=checkout`.
2. Confirm the 5m burn-rate is still ≥ 14.4× (i.e. the issue is **still
   happening**, not a 10-minute spike that already cleared).
3. Look at "Request Rate by Status Class". Is the 5xx series the dominant
   contributor? If 4xx, you have a different problem (client / auth
   regression) and this alert is a misfire.
4. Look at the payments stub `/admin/inject` state:
   `curl -s http://localhost:8081/admin/inject`. If `fail_ratio > 0`,
   you (or a labmate) left fault injection on. Reset it:
   `curl -X POST http://localhost:8081/admin/inject -H 'content-type: application/json' -d '{"fail_ratio": 0.0, "latency_ms": 30}'`.

### Mitigation options

| Option | When to use | Side-effect |
|--------|-------------|-------------|
| Roll back the latest checkout deploy | Symptom started right after a deploy | Lose the change |
| Drop fault injection on payments | Lab-only path: someone left it on | None |
| Open a circuit on the payments client | Payments is unavailable, not the checkout | All checkouts return 503 immediately, but the checkout service stays healthy |
| Page the payments owner | The fault is in payments, not us | Out of scope for this lab |

### Stand-down criteria

Resolve when:

- 5m burn rate has been < 1.0 for 5 minutes, AND
- 1h burn rate is trending down.

Do **not** stand down on the 5m alone — a brief recovery during a
flapping outage is not the same as recovery.

## Ticket: `SLOSlowBurnAvailability`

**Why you got a ticket.** The 6h burn rate AND the 30m burn rate are
both ≥ 6×. We are not on fire, but we are leaking budget faster than
budget regenerates. Investigate during business hours; do not page on
this.

Same triage steps, but with a wider time horizon (look at the 6h panel,
not the 5m panel). Common causes for slow burn that fast burn missed:

- Periodic batch job creating 0.5–2% extra error rate at scheduled
  intervals.
- A flaky downstream that 5xxs once every few seconds.
- Histogram bucket boundary mismatched with the SLO threshold (latency
  SLI only).

## Pager: `SLOFastBurnLatency`

**Why you were paged.** Both the 1h and the 5m latency burn rates are
≥ 14.4×. Far more than 0.1% of checkouts are taking longer than 300 ms.

**The trap:** requests are still succeeding. The error-ratio stat panel
will look green and the status-class panel will show pure 2xx. If you
triage this like an availability incident you will conclude nothing is
wrong. Go to the latency panel first.

### Triage in two minutes

1. Open the latency panel on
   `http://localhost:3000/d/red-overview/red-overview?var-service=checkout`
   and confirm p95/p99 are above 300 ms and still climbing or flat — not
   already recovering.
2. Check `inflight_requests`. If it is climbing, the service is
   accumulating concurrency and this will become an availability
   incident when timeouts start firing (`PAYMENTS_TIMEOUT_S` = 2.0s, so
   checkout starts returning 504 once payments exceeds that).
3. Check whether the slowness is downstream:
   `curl -s http://localhost:8081/admin/inject`. A non-zero `latency_ms`
   means fault injection is still on. Reset with
   `curl -X POST http://localhost:8081/admin/inject -H 'content-type: application/json' -d '{"fail_ratio": 0.0, "latency_ms": 30, "latency_jitter_ms": 20}'`.
4. If payments is clean, the latency is being added by checkout itself —
   check CPU (`docker stats hlsa2-checkout`). A single uvicorn worker
   saturates at roughly 90 rps.

### Mitigation options

| Option | When to use | Side-effect |
|--------|-------------|-------------|
| Reset payments fault injection | Lab path: someone left latency injected | None |
| Scale checkout workers / replicas | CPU near 100% of a core | Costs capacity; does not help if payments is the slow part |
| Shed load at the edge | Latency is caused by overload, not a code path | Rejected requests become 4xx/5xx — trades the latency SLO against the availability one, so decide deliberately |
| Reduce `PAYMENTS_TIMEOUT_S` | Slow payments is holding connections open | Converts slow successes into fast 504s: latency budget stops burning, availability budget starts. Only do this knowingly |

### Stand-down criteria

Resolve when the 5m latency burn rate has been < 1.0 for 5 minutes **and**
the 1h rate is trending down. Do not stand down on p99 alone — p99 is
interpolated and can drop while the fraction of requests over 300 ms is
still elevated.

## Ticket: `SLOSlowBurnLatency`

**Why you got a ticket.** The 6h and 30m latency burn rates are both
≥ 6×. A persistent tail-latency regression is leaking budget without
being dramatic enough to page.

Same triage with a wider horizon. Causes that fit this shape:

- A downstream that got quietly slower after its own deploy — payments
  latency crept from 30 ms to 200 ms and nothing errored.
- Gradual data growth pushing a query past a threshold.
- **An instrumentation change, not a real regression.** If someone
  altered `LATENCY_BUCKETS` in `checkout/main.py` and the `0.3` boundary
  is gone, the SLI numerator is now interpolated and the measured value
  will shift even though the service did not. Check git history on the
  histogram definition before chasing a phantom.

## Anti-patterns to avoid

- **Don't acknowledge and walk away.** Acknowledging silences the alert
  but does not fix the burn. The error budget keeps draining.
- **Don't tweak the alert threshold to make it stop.** If the alert is
  firing on real user pain, the problem is the system, not the alert.
- **Don't promote per-request labels to make debugging easier.** Use
  Loki / traces. The metric pipeline is for SLO bookkeeping.
