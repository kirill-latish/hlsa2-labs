# Runbook: full 503 outage from a brief dependency blip (deep-health-check cascade)

> Page trigger: alert `NoHealthyBackends`
> (`lab61:backends_up:count < 1`) coinciding with a `503` spike in
> `lab61_edge_5xx_total`.

## 1. Confirm scope (60 s)

- **Edge Overview** dashboard, "Backend health state" panel: did ALL
  backends go down at the *same instant*? Simultaneity is the signature
  of a shared-dependency cascade (not independent instance failures).
- "5xx by class" panel: a 503 spike (no healthy backends), not 502/504.

## 2. Identify the failure domain (60 s)

- `make edge-status` -> is `health_depth` = `deep`? A deep check queries
  the shared Postgres dependency.
- Check the dependency: if Postgres had a brief blip, every backend
  failed its deep check together and the balancer was left with zero
  healthy backends - converting a few-second dependency hiccup into a
  full service outage.

## 3. Stop the bleeding (2 min)

1. **Switch to a shallow check immediately**:
   `make apply-fix CANDIDATE=shallow-healthcheck`. Backends stop being
   ejected for a dependency blip; the service rides out the next blip.
   Verify `lab61:backends_up:count` returns to 4.
2. If the dependency itself is down, that is a separate incident - page
   the dependency owner. The shallow check only stops the *cascade*; it
   does not fix a genuinely-down dependency.

## 4. Root cause (after mitigation)

- `perf/results/healthcheck-deep/report.md` - `min_healthy_during == 0`
  proves the cascade; compare with
  `perf/results/healthcheck-shallow/report.md`
  (`min_healthy_during == 4`).
- `perf/results/healthcheck-shallow/compare-vs-deep.md` - the side-by-side.

## 5. Residual trade-off + postmortem

- A shallow check **misses a process that is up but genuinely broken**
  downstream. The right answer is usually: shallow liveness for the load
  balancer's rotation decision, plus a *separate* deep readiness/
  dependency check that pages without ejecting every backend at once.
- [ ] Is any production health check querying a shared dependency in a
      way that can eject all replicas simultaneously?
- [ ] Are liveness and dependency checks separated?
- [ ] Does the dependency have its own redundancy / circuit breaker?
