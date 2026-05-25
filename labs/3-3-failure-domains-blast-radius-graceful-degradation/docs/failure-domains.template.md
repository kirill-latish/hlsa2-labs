# Failure domains & dependency classification

> Step 3 of topic-246 wants this filled in BEFORE you run any
> benchmarks. The classification is what makes "graceful degradation"
> a measurable goal instead of a vibe.
>
> Copy this template to `docs/failure-domains.md` and fill the TODOs.

## Domains

| Domain                | Shares fate with                                  | Independent of                          |
| --------------------- | -------------------------------------------------- | --------------------------------------- |
| `gateway`             | the process the gateway runs in                    | every dep (different processes)         |
| critical-deps pool    | `price`, `cart` (they share the inbound request)   | non-critical deps                       |
| non-critical pool     | `recommendations`, `reviews`, `recently-viewed`    | critical deps                           |
| `fault-injector`      | nothing (control-plane only)                       | data plane                              |

## Dependency classification

| Dep              | Critical? | Why                                                                                                                                  | Acceptable degraded mode                                       |
| ---------------- | --------- | ----------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------- |
| `price`          | YES       | TODO: explain why pricing is on the critical path                                                                                    | None - fail fast                                               |
| `cart`           | YES       | TODO: explain why cart is on the critical path                                                                                       | None - fail fast                                               |
| `recommendations`| NO        | TODO: explain why recommendations is a non-critical widget                                                                           | Serve LKG or omit the widget                                   |
| `reviews`        | NO        | TODO: explain why review preview is a non-critical widget                                                                            | Serve LKG or omit the widget                                   |
| `recently-viewed`| NO        | TODO: explain why recently-viewed is a non-critical widget                                                                           | Serve LKG or omit the widget                                   |

## SLO summary

- **Critical-journey success ratio (degraded counts as success)**: TODO (target, e.g. 99.5%)
- **Per-dep success ratio (informational only)**: TODO (track but does not gate the SLO)

## Visual

(Optional: paste the ASCII output of `make show-topology` here.)

```
TODO: paste perf/results/topology-<timestamp>.txt
```
