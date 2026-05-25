# Runbook: critical-journey success below SLO

> Page trigger: alert `CheckoutSuccessBelowSLO`
> (`lab33:critical_journey:success_ratio < 0.99 for 2m`).

## 1. Confirm scope (60 s)

- Open the **Resilience Overview** dashboard.
- Row 1: is the success-ratio drop coming from `success_full -> failed`
  (true outage) or `success_full -> success_degraded` (graceful)?
- Row 2: which dep's `lab33:dep:success_ratio:1m` dropped?

## 2. Identify the failure domain (60 s)

- Cross-reference with `lab33_fault_active{}`. If a fault is active,
  this is a lab-injected incident; otherwise check the dep's logs.
- If the affected dep is **critical** (`price` or `cart`): page that
  dep's on-call. Do NOT enable fallback for critical deps - it would
  return wrong data.
- If the affected dep is **non-critical**: continue.

## 3. Stop the bleeding (2 min)

Pick the smallest intervention that restores SLO:

1. **Is `FALLBACK=off`?** Enable it and redeploy the gateway. The
   non-critical widget will be served from LKG cache or omitted from
   the response. Verify by watching `lab33_gateway_fallbacks_served_total`.
2. **Is `BULKHEAD=off`?** Enable it. Pool exhaustion of the slow dep
   was starving the critical path.
3. **Is `CIRCUIT_BREAKER=off`?** Enable it. The gateway will fast-fail
   the dead dep instead of hanging on it.

If none of the above restores SLO, the dep itself is in a bad state
that resilience controls cannot mask. Escalate to the dep owner.

## 4. Root cause (after mitigation)

Once SLO is back, capture:

- `perf/results/active-fault.txt` (or "none" for organic incidents).
- `lab33_gateway_dep_calls_total` per outcome - which outcomes were
  elevated (`timeout`, `error`, `breaker_open`)?
- Pool utilization at the time of incident.

Use the data to update `docs/failure-domains.md` if the dep needs to
be re-classified.

## 5. Postmortem checklist

- [ ] Was the dep classification correct, or did a critical dep
      surprise us?
- [ ] Was the right resilience control on by default? If not, ship a
      change.
- [ ] Did the alert fire fast enough? (`for: 2m` threshold reasonable?)
- [ ] Did we have a runbook for this exact failure mode? If not,
      write one.
