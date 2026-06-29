# Architecture review - lab 6-2 (CDN strategy and edge delivery)

> Copy to `docs/review.md` and fill the TODOs.
> Target: ~1,500-2,000 words. Every quantitative claim must cite a file
> under `perf/results/` or `docs/img/`, and every cache-key change must
> report its cross-user leak probe result. `make check-submission`
> enforces that every cited filename exists.

For each experiment answer the four questions: **what did you measure**
(cite the artifact), **what changed**, **why** (the mechanism), and
**what new risk** the change introduces.

---

## 1. Environment & method

- **Captured**: `perf/results/env.txt` and `perf/results/meta.json`
- **Workload model**: `perf/workload.json` (baseline at TODO RPS for TODO; ~30% of static+page traffic carries tracking params)
- **Edge**: 3 PoPs + 1 origin shield, all the instrumented Go caching proxy (`cmd/cache-proxy`), one binary, role selected by env. Origin: `cmd/origin`.
- **Lab version**: TODO (git rev) - see `perf/results/meta.json`
- **Host**: TODO (single laptop / VM / etc.)

Method: each labelled condition snapshots every node's `/metrics` before
and after the run, so all figures are per-run deltas. Baseline is run 3x
for run-to-run sigma (`make bench-baseline RUNS=3`).

## 2. Baseline edge cache ratio (by request AND by bytes)

Citation: `perf/results/baseline/report.md`

| metric                  | median | sigma |
| ----------------------- | -----: | ----: |
| hit ratio by request    | TODO % | TODO  |
| hit ratio by bytes      | TODO % | TODO  |
| offload by request      | TODO % | TODO  |
| offload by bytes        | TODO % | TODO  |

- The two hit ratios differ because TODO (popular objects are small =
  cheap hits; the rare long-tail objects are large = expensive misses,
  so origin BYTES stay high even at a high by-request ratio).
- Origin cost is driven by bytes, so the by-bytes number is the one the
  finance conversation hangs on.
- Dashboard screenshot: `docs/img/baseline-edge.png`

## 3. Cache-key fragmentation and the strip-tracking-params fix

Setup: `make bench-cachekey KEY=full-querystring LABEL=cachekey-full`,
then `make apply-fix CANDIDATE=strip-tracking-params`, then
`make bench-cachekey KEY=stripped LABEL=cachekey-stripped`.

Citation: `perf/results/cachekey-full/report.md`,
`perf/results/cachekey-stripped/report.md`, and the `compare-cachekey`
output.

| metric                  | full-querystring | stripped |
| ----------------------- | ---------------: | -------: |
| hit ratio by request    | TODO %           | TODO %   |
| cache-entry cardinality | TODO             | TODO     |

- What changed: TODO (hit ratio jumped from TODO to TODO; cardinality
  collapsed from TODO to ~the true resource count).
- Why: every cache-key dimension that does NOT change the response
  (utm_source/fbclid/gclid) fragments identical content into a separate,
  guaranteed-miss entry per variant.
- New risk: TODO (stripping a param that DOES change the response would
  serve wrong content - the allowlist must be correct).

## 4. The cross-user leak and its elimination (security)

Setup: `make apply-fix CANDIDATE=broad-key-personalized`,
`make probe-cross-user LABEL=leak-before`,
`make apply-fix CANDIDATE=private-personalized`,
`make probe-cross-user LABEL=leak-after`, `make verify-cacheability`.

Citation: `perf/results/leak-before/summary.json`,
`perf/results/leak-after/summary.json`, `perf/results/cacheability.txt`.

| metric            | leak-before | leak-after |
| ----------------- | ----------: | ---------: |
| leaked responses  | TODO        | 0          |

- What changed: the leak count went from TODO to 0.
- Why: the shared edge cache serves whatever is stored under a key to
  every requester with that key; caching a personalized response under a
  key that ignores identity hands one user's data to others.
- The fix: TODO (marked private/no-store so it never enters the shared
  cache, OR added identity to the key). `make verify-cacheability`
  confirms genuinely-shared static content is still a HIT.
- New risk: TODO (per-user keys eliminate cross-user sharing entirely -
  no offload for that content).

## 5. The origin thundering herd: shielding off vs on (+ stale-if-error)

Setup: `make apply-fix CANDIDATE=shield-off`,
`make expire-popular-object LABEL=shield-off`,
`make analyze-fanin LABEL=shield-off`; then `CANDIDATE=shield-on`,
repeat; `make compare-fanin BEFORE=shield-off AFTER=shield-on`. Then
`make apply-fix CANDIDATE=stale-if-error` and
`make inject-origin-outage LABEL=stale-if-error`.

Citation: `perf/results/shield-off/fanin.md`,
`perf/results/shield-on/fanin.md`,
`perf/results/stale-if-error/summary.json`.

| metric                       | shield-off | shield-on |
| ---------------------------- | ---------: | --------: |
| origin fan-in on expiry      | TODO       | TODO      |

- What changed: fan-in collapsed from ~TODO (PoP count) to ~1.
- Why: request collapsing (singleflight WITHIN a PoP) bounds each PoP to
  one in-flight upstream fetch; origin shielding (the shared mid-tier
  cache ACROSS PoPs) collapses the PoPs' misses into ~one origin fetch.
- stale-if-error: `perf/results/stale-if-error/summary.json` shows the
  edge served TODO STALE responses during the origin outage instead of
  erroring.
- New risk: TODO (a hop of latency for cold content; the shield is a new
  shared dependency).

## 6. The 'caching nothing' silent failure

Setup: `make inject-setcookie-on-static LABEL=bypass`,
`make analyze-cache-status LABEL=bypass`.

Citation: `perf/results/bypass/cache-status.md`,
`docs/img/cache-status.png`.

- What changed: the BYPASS share spiked to TODO% while HIT collapsed.
- Why: a Set-Cookie (or no-cache / Vary: Cookie) on cacheable content
  makes every edge treat it as uncacheable - the site works, latency and
  uptime look normal, but offload collapses and you pay for a cache that
  caches nothing.
- Why latency/uptime miss it: TODO. The signal is the cache-status
  distribution; monitor BYPASS/MISS by route, not just latency and
  uptime.
- New risk: TODO.

## 7. Decision ladder + residual risks

Use the topic's decision ladder to justify the edge strategy you'd
recommend for a given workload shape:

- **No CDN** - TODO (when the workload is tiny / all-dynamic / private).
- **Static assets only** - TODO.
- **+ semi-dynamic** (short TTL, careful key) - TODO.
- **+ origin shielding** (popular-object herds) - TODO.
- **Edge compute** - TODO (personalization at the edge without leaking).

Residual risks for a production runbook:

- Purge propagation is eventual (a purge is not instantaneous edge-wide).
- Purge-everything stampedes the origin (mass simultaneous cold misses).
- Identity-in-key kills cross-user sharing (no offload for that content).
- The allowlist must stay correct as the app adds content-affecting params.

Runbooks: `runbooks/thundering-herd-incident.md`,
`runbooks/cache-leak-incident.md`.

---

## Reproducibility note

```
cp .env.example .env
make up && make seed && make env-fingerprint && make edge-status
make bench-baseline RUNS=3 DURATION=5m && make analyze-baseline
make bench-cachekey KEY=full-querystring DURATION=3m LABEL=cachekey-full
make apply-fix CANDIDATE=strip-tracking-params
make bench-cachekey KEY=stripped DURATION=3m LABEL=cachekey-stripped
make compare-cachekey BEFORE=cachekey-full AFTER=cachekey-stripped
make apply-fix CANDIDATE=broad-key-personalized && make probe-cross-user LABEL=leak-before
make apply-fix CANDIDATE=private-personalized && make probe-cross-user LABEL=leak-after
make verify-cacheability && make compare-leak BEFORE=leak-before AFTER=leak-after
make apply-fix CANDIDATE=shield-off && make expire-popular-object LABEL=shield-off && make analyze-fanin LABEL=shield-off
make apply-fix CANDIDATE=shield-on  && make expire-popular-object LABEL=shield-on  && make analyze-fanin LABEL=shield-on
make compare-fanin BEFORE=shield-off AFTER=shield-on
make apply-fix CANDIDATE=stale-if-error && make inject-origin-outage LABEL=stale-if-error
make inject-setcookie-on-static LABEL=bypass && make analyze-cache-status LABEL=bypass
make check-submission
```
