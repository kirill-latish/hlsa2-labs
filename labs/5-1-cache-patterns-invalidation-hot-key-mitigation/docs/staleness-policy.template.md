# Cache staleness policy - lab 5-1

> Step 7 of topic-250 asks you to document the freshness budget your
> application has accepted. Copy this to `docs/staleness-policy.md` and
> fill the TODOs. This is the contract downstream consumers must tolerate.

## Chosen invalidation strategy

- **Strategy**: TODO (`ttl-only` or `explicit-invalidate`)
- **Base TTL**: TODO seconds
- **Why this strategy for this data**: TODO (one paragraph - how fresh
  must this data be, and what does the business cost of staleness look
  like?)

## Measured staleness (from the writer/reader race)

Citation: `perf/results/staleness/report.md`.

| strategy            | fraction_stale | max_staleness_seconds |
| ------------------- | -------------: | --------------------: |
| ttl-only            | TODO %         | TODO                  |
| explicit-invalidate | TODO %         | TODO                  |

## The freshness budget downstream consumers must tolerate

- **Worst-case staleness window**: TODO seconds (bounded by TODO)
- **Typical staleness**: TODO
- **Consumers that CANNOT tolerate this** (and what they must do
  instead, e.g. read-through to the SoR): TODO

## Failure mode: incomplete writer coverage

Explicit invalidation only works if **every** write path invalidates.
Describe what happens if one path (a batch job, an admin tool, a second
service writing the same table) updates the SoR without invalidating:

- Observable symptom: TODO
- Detection: TODO (e.g. periodic cache-vs-source audit like the lab's
  staleness probe)
- Mitigation: TODO (belt-and-suspenders TTL as a backstop, CDC-driven
  invalidation, etc.)
