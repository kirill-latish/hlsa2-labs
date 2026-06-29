# Runbook: poison-message incident

> Page trigger: alert `PoisonRetryStorm`
> (`lab52:retries:rate1m > 100 and lab52:acked:cluster:rate1m < 10`).

## Symptoms

- Cluster throughput (`lab52:acked:cluster:rate1m`) collapses toward
  zero while the retry rate (`lab52:retries:rate1m`) spikes.
- Consumer lag - count (`lab52_consumer_lag_count`) climbs and lag -
  time (`lab52_oldest_unprocessed_age_seconds`) grows: healthy messages
  are queued behind a consumer slot that is wedged.
- A single `message_id` is redelivered over and over (visible in
  consumer logs).

This is the canonical **poison-message** failure mode: a message the
consumer can never process, under **unbounded retries**, occupies a
consumer slot forever (read -> fail -> nack/requeue -> read again).

## 1. Confirm the cause (60 s)

- Open the **Pipeline Overview** dashboard. Row 1 (lag count + time) and
  row 2 (throughput) show the collapse together - throughput alone hides
  the lag growth.
- Confirm `lab52_consumer_mode{mode="unbounded-retry"} == 1`.

## 2. Stop the bleeding (2 min)

- Apply bounded retries + DLQ:
  `make apply-fix CANDIDATE=bounded-retry MAX_RETRIES=5`.
- The wedged message is now retried a bounded number of times with
  exponential backoff, then routed to the DLQ. The consumer slot frees;
  `lab52:acked:cluster:rate1m` returns to baseline within ~30 s and lag
  drains.
- Verify the message reached the DLQ: check `lab52_dlq_total` and the
  `lab52.dlq` queue depth (`make brokers-status`).

## 3. Classify (5 min)

- If failures are a mix of transient and permanent, prefer
  `make apply-fix CANDIDATE=classify-failures` so permanent failures go
  straight to the DLQ without burning retry attempts.

## 4. Drain the DLQ

- Inspect dead-lettered messages. For genuinely-broken (permanent)
  messages: fix the producer or schema and discard. For transient
  messages that exhausted retries: requeue after the downstream
  recovers.

## 5. Postmortem checklist

- [ ] Was MAX_RETRIES set appropriately? Small N may DLQ recoverable
      messages prematurely; large N wastes capacity on broken messages.
- [ ] Did we have a DLQ at all, or did unbounded retries have no escape?
- [ ] Cite the before/after evidence: `perf/results/poison-baseline/`
      vs `perf/results/poison-after/` and
      `perf/results/poison-after/compare-vs-before.md`.
