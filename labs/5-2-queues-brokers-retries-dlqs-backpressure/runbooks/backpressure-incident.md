# Runbook: backpressure / unbounded-lag incident

> Page trigger: alert `ConsumerLagGrowing`
> (`lab52_consumer_lag_count > 5000 for 2m`).

## Symptoms

- Consumer lag - count (`lab52_consumer_lag_count`) climbs steadily and
  does not plateau; lag - time grows with it.
- Cluster throughput is at or near capacity but the **producer rate
  exceeds it** - the producer is winning the race.
- Producer error rate (`lab52:producer_errors:rate1m`) is either:
  - **non-zero** -> the broker's bounded queue is rejecting publishes and
    the signal is reaching the producer (backpressure is propagating), or
  - **zero** -> the producer is unaware; lag will grow until the queue
    bound is hit and messages are dropped or the broker melts.

This is the **sustained-overload** failure mode. A burst is absorbed by
the buffer; sustained overload reveals whether backpressure actually
propagates end-to-end.

## 1. Confirm the regime (60 s)

- Open the **Pipeline Overview** lag-over-time pane
  (`docs/img/backpressure-lag.png`). Stabilizing curve = backpressure
  honored; straight-up-and-to-the-right = ignored.
- Check `lab52_producer_backpressure_enabled`.

## 2. Stop the bleeding (2 min)

- Enable backpressure propagation:
  `make apply-fix CANDIDATE=backpressure-signal`. The work queue is
  bounded (`x-max-length` + `x-overflow=reject-publish`); the producer
  now honors publish rejections (publisher confirms) and slows.
- Lag should stabilize at a predictable level rather than growing
  without bound. The system degrades gracefully: it sheds offered load
  at the producer instead of melting the broker.

## 3. Address the root cause (longer term)

- If lag stabilized only because the producer slowed, capacity is still
  the real constraint - scale the consumer fleet (add instances) or make
  the downstream Postgres write cheaper.
- If the producer ignored backpressure, fix the producer to handle
  HTTP 429 / Kafka quota throttling / publish nacks. A producer that
  ignores backpressure is a future outage in the making.

## 4. Validate

- `lab52_consumer_lag_count` plateaus (does not climb monotonically).
- `lab52:acked:cluster:rate1m` is sustained near capacity.
- Cite `perf/results/backpressure/report.md` and the lag-over-time
  screenshot in the review.

## 5. Postmortem checklist

- [ ] Was the overload organic (traffic spike) or self-inflicted
      (retry amplification, batch job)?
- [ ] Is the queue bound sized correctly for the worst tolerable lag?
- [ ] Does every producer in the chain honor backpressure, or only some?
