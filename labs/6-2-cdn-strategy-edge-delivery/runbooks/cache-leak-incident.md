# Runbook: cross-user content leak from the shared edge cache

> Page trigger: alert `CrossUserLeak` (`increase(lab62_cross_user_leak_total[5m]) > 0`),
> or a user report of seeing another user's account data.

> This is a security incident. Treat it as a data breach: a shared cache
> served one user's personalized response to another.

## 1. Contain immediately (first 5 min)

- Identify the leaking route (personalized/authenticated content being
  cached). On this lab it is `/account`.
- Make the route uncacheable at the edge NOW:
  `make apply-fix CANDIDATE=private-personalized` (sets the PoPs'
  personalized_mode to `private-no-store`), then purge any cached copies:
  POST `{"path":"/account"}` to `/admin/purge` on every PoP, or
  `/admin/flush` if unsure of the key.
- Re-run `make probe-cross-user LABEL=leak-after` and confirm the leak
  count is **0** before standing down.

## 2. Confirm scope (who saw what)

- `lab62_cross_user_leak_total` gives the count observed by the probe;
  production logs give the real blast radius.
- Determine which keys cached personalized content: any response with
  `X-Personalized` (or `Set-Cookie` / `Cache-Control: private`) that was
  nonetheless stored is suspect.

## 3. Verify you did not over-correct

- `make verify-cacheability` must still show genuinely-shared static
  content as a HIT. Marking *everything* private to stop the leak trades
  a breach for an origin meltdown - fix only the personalized routes.

## 4. Mechanism reminder

The edge cache is **shared**: anything stored under a key is served to
everyone who computes that key. Personalized content must therefore
either (a) carry identity in the cache key (per-user entries, no
cross-user sharing), or (b) be marked `private` / `no-store` so it never
enters the shared cache. Option (b) is the usual answer; option (a)
trades the leak for zero offload on that content.

## 5. Postmortem checklist

- [ ] Why did a personalized response get a cacheable key? (broad key
      ignoring the auth cookie, a missing `private`/`no-store`, a
      Vary misconfig?)
- [ ] Add a guard test that probes cross-user leakage in CI.
- [ ] Add the `CrossUserLeak` alert to the on-call rotation if missing.
- [ ] Review every route that reads identity for the same class of bug.
