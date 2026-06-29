# Runbook: backend failover not completing (edge routing to a dead backend)

> Page trigger: elevated `lab61_edge_5xx_total{code="502"}` and/or one
> backend stuck with `lab61_edge_backend_up == 1` while erroring.

## 1. Confirm scope (60 s)

- Open the **Edge Overview** dashboard.
- "Backend health state" panel: is one backend flapping or stuck `up`
  while the "5xx by class" panel shows a 502 climb?
- `make edge-status` - read `healthy`, `consecutive_fails`, and
  `requests` per backend.

## 2. Identify the failure domain (60 s)

- A 502 climb means the proxy is still routing to a backend it cannot
  reach (connection refused). Detection lag = health-check
  `interval x threshold`. Read the active values from `make edge-status`.
- If detection is slow because the interval/threshold are large, that is
  the bug, not the backend.

## 3. Stop the bleeding (2 min)

Pick the smallest intervention:

1. **Tighten detection**: `make apply-fix CANDIDATE=fast-healthcheck
   INTERVAL=2s THRESHOLD=2`. The dead backend leaves rotation in ~4s
   instead of up to 30s. Verify `lab61_edge_backend_up` for the bad
   backend drops to 0.
2. **Force it out** if a specific instance is known bad:
   `make inject-backend-failure BACKEND=backend-N` is the lab handle;
   in production, drain/cordon the instance.
3. Confirm the surviving backends absorb the load on the per-backend
   distribution panel (no single survivor saturating).

## 4. Root cause (after mitigation)

- `perf/results/<label>/report.md` - measured detection time vs
  expected (`interval x threshold`).
- Dropped-request (502) count during the detection window.
- Were checks shallow or deep? (`make edge-status` -> `health_depth`).

## 5. Residual trade-off + postmortem

- Faster health checks increase check load on every backend and the risk
  of **flapping** on a marginal backend that is briefly slow. Pick the
  interval/threshold that detects real failures without flapping on
  jitter.
- [ ] Was detection time within SLO?
- [ ] Did one survivor saturate after redistribution? Re-check capacity.
- [ ] Is the health-check interval/threshold documented per service?
