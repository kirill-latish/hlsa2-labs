# Runbook: cache stampede (database melts at TTL expiry)

> Page trigger: alert `CacheStampedeFanIn`
> (`lab51:cache:fan_in:rate1m > 5 for 1m`), usually correlated with a
> Postgres CPU / connection-pool spike on a regular cadence (every TTL).

## 1. Confirm scope (60 s)

- Open the **Cache Overview** dashboard.
- Stampede signature: the **fan-in ratio** panel spikes periodically and
  the **source-fetch rate** briefly equals the request rate while the
  **hit ratio** dips. Read p99 jumps from sub-ms to seconds at each dip.
- Cross-check the cadence against the configured TTL
  (`curl http://localhost:8080/admin/config`). Spikes every `TTL`
  seconds == synchronized expiry of a hot key.

## 2. Stop the bleeding (2 min)

Pick the smallest intervention that flattens the fan-in:

1. **Is `coalescing=none`?** Enable request coalescing:
   `make apply-fix CANDIDATE=singleflight`. The first miss does the
   fetch; concurrent misses for the same key wait and reuse it. Fan-in
   collapses toward 1.
2. **Is `ttl_jitter_pct=0`?** Turn jitter on
   (`make apply-fix CANDIDATE=jitter JITTER=20pct`). This desynchronises
   expiry *across* keys; it does NOT help a single hot key, so pair it
   with coalescing.
3. **Repeatedly-hot single key?** Prefer `xfetch` (probabilistic early
   refresh - the key never hard-expires) or `swr` (serve stale while
   revalidating) so the hot path never blocks on the SoR at all.

Verify recovery on the fan-in and source-fetch panels.

## 3. Root cause (after mitigation)

- `perf/results/<label>/fan_in_ratio.json` - the fan-in you observed.
- Which key(s) expired together? (Single hot key vs a cohort written at
  the same time and thus expiring together.)
- Was the SoR fetch latency the amplifier? (Longer fetch == wider window
  == more concurrent misses per expiry.)

## 4. Residual risk of the fix you chose

- **singleflight**: all waiters share the fate of the one in-flight
  fetch - if it errors, they all error. Add a short negative-cache or
  per-waiter timeout.
- **xfetch**: needs probability/beta tuning; too timid and it still
  expires, too eager and you over-refresh.
- **swr**: serves slightly stale data during the refresh window -
  acceptable only within the documented staleness budget.

## 5. Postmortem checklist

- [ ] Was coalescing off by default when it should have been on?
- [ ] Is the TTL appropriate for this key's write rate?
- [ ] Did the alert fire before customers noticed the p99 spike?
- [ ] Is there a hotter-than-a-shard key that also needs local LRU?
