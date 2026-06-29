# Runbook: origin thundering herd on popular-object expiry

> Page trigger: alert `EdgeHitRatioCollapsed` together with a spike on
> the **Origin request rate** panel, typically right after a popular
> object's TTL elapses or a purge.

## 1. Confirm scope (60 s)

- Open the **Edge Delivery** dashboard.
- Is the **Origin request rate** panel spiking while **Cache-status
  distribution** shows a burst of MISS/EXPIRED across PoPs?
- Check `lab62:origin:requests:byclass:rate1m` - is it one object class
  (e.g. `static`) dominating? That is the hot object.

## 2. Stop the bleeding (2 min)

Pick the smallest intervention that bounds origin fan-in:

1. **Is origin shielding off?** Turn it on:
   `make apply-fix CANDIDATE=shield-on`. The shield collapses all PoP
   misses for the same object into ~one origin fetch.
2. **Is request collapsing off on the PoPs?** Re-enable it (POST
   `{"request_collapsing":true}` to each PoP). Within a PoP, a burst of
   concurrent misses for one key should become a single upstream fetch.
3. **If the origin is already overwhelmed**, enable `stale-if-error`
   (`make apply-fix CANDIDATE=stale-if-error`) so the edge serves
   last-known-good content while the origin recovers.

Verify with `make expire-popular-object LABEL=incident` +
`make analyze-fanin LABEL=incident`: fan-in should drop to ~1.

## 3. Root cause (after mitigation)

- Was a `purge-everything` issued? Mass simultaneous cold misses are a
  self-inflicted herd. Stagger purges or rely on TTL.
- Did the popular object have too short a TTL for its request rate?
- Was the shield a single point of failure (one shield, no failover)?

## 4. Mechanism reminder

Request collapsing bounds fan-in **within** a PoP; origin shielding
bounds it **across** PoPs. Composed, origin fan-in for one object is ~1
fetch regardless of PoP count. The cost is one extra hop of latency for
genuinely-cold content and a new shared dependency (the shield).

## 5. Postmortem checklist

- [ ] Were shielding + request collapsing on by default? If not, ship it.
- [ ] Is there a second shield / failover for the shield tier?
- [ ] Did the alert fire before the origin saturated?
- [ ] Is there a purge policy that avoids purge-everything stampedes?
