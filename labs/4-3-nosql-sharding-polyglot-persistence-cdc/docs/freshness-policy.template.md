# Freshness policy — your decisions

Copy this file to `docs/freshness-policy.md` and fill out one row per surface.

| API surface                       | Reader                | Policy            | Justification                                |
|-----------------------------------|-----------------------|-------------------|----------------------------------------------|
| `GET /products/:id` (cart)        | Postgres SoR          | `read-from-sor`   | Pricing must be authoritative.               |
| `GET /search/products?q=...`      | Elasticsearch         | `read-from-derived` | UX value > a 1s stale facet.               |
| `GET /products/:id` (PDP, my-cart)| ES + lsn-wait fallback| `lsn-wait`        | Sub-second wait acceptable, then SoR fallback. |

For each row also document:

- **Worst-case staleness** (median, p99) measured under base load.
- **Behaviour during a CDC outage** (does the policy fail closed? does it
  fall back to SoR? does it return 503?).
- **Operational signal** that triggers a runbook (a Prometheus alert
  expression, a synthetic probe, an on-call ping).

A policy isn't done until you can answer these three questions for it.
