# Runbook: retry storm

> Page trigger: alert `RetryStorm`
> (`lab33:retry_amplification:1m > 0.5 for 1m`).
> Also fires when the offered-vs-served gap diverges past peak capacity.

## Symptoms

- Loadgen / clients show many retries per served request
  (`lab33:retries:rate1m` >> `lab33:served:rate1m`).
- Gateway p99 latency climbs even though dep p99 is flat or falling
  (because requests queue inside the gateway).
- Inbound 429s climb (`lab33_gateway_shed_total{scope="inbound_429"}`).

This is a **metastable feedback loop**: more failures -> more
retries -> more load -> more failures. The mechanism stops only by
removing retries OR removing inbound load.

## 1. Apply the load-shed circuit (60 s)

- Confirm `lab33_gateway_control_enabled{control="LOAD_SHED"}` == 0.
- Roll the gateway with `LOAD_SHED=on`. Inbound requests over the
  `MAX_INFLIGHT` ceiling will receive `429 Too Many Requests`.
- Within 60 s the gateway should drain.

## 2. Cap retry amplification (2 min)

- Confirm `lab33_gateway_control_enabled{control="RETRY_BUDGET"}` == 0.
- Roll the gateway with `RETRY_BUDGET=on`. Retries are now bounded
  to a global token bucket (10% of inbound RPS); excess retries are
  denied (`lab33_gateway_retries_total{outcome="denied_budget"}`).

## 3. Stop client-side amplification (if any)

- If the loadgen, mobile clients, or upstream services issue their
  own retries on 5xx, contact the client owner. Even with the gateway
  budget enabled, *client* retries can re-amplify.

## 4. Validate

- `lab33:offered:rate1m` should match (or trail) `lab33:served:rate1m`.
- `lab33:retry_amplification:1m` should fall below 0.5.
- Critical-journey success ratio should recover within 2 min.

## 5. Postmortem

- Was the inbound surge organic or driven by retries? Plot the offered
  rate vs upstream traffic source.
- Did we have capacity headroom? Plot pool utilization.
- Add a chaos experiment in the lab to re-create this incident under
  the storm/tamed comparison; cite the resulting before/after report.
