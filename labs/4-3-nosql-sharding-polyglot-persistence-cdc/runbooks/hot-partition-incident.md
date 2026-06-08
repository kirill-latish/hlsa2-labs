# Runbook — Hot partition (one shard hot, others idle)

## Detect

- Grafana → **Lab 4-3 — Polyglot Overview** → "Skew: max/mean per collection"
  > 1.5 for >2m fires `HotShardSuspected`.
- Per-shard insert rate panel: one shard at >2× the rest.
- Symptom in the bench: `partition_metrics.json -> max_to_mean > 1.5`.

## Triage (< 5 min)

1. Identify which collection is skewed (`{collection}` label on the alert).
2. From `partition-stats`, read the per-shard `lab43_collection_doc_count` —
   confirm only one shard is growing.
3. Quick-look top tenants on the hot shard:

   ```bash
   docker compose exec -T mongos-1 mongosh --quiet --eval '
     db.getSiblingDB("lab43").events_candidate.aggregate([
       {$group:{_id:"$tenant_id", n:{$sum:1}}},
       {$sort:{n:-1}}, {$limit:5}
     ]).toArray()
   '
   ```

## Mitigate (next 15 min)

If the hot tenant is small enough that you can rate-limit at the gateway —
do that first; it's reversible. Otherwise:

- **Hash-suffix the hot tenant**: requires no data migration. Switch the
  loadgen / writers to `events_hash_suffix` and route writes for the hot
  tenant into N buckets. Reads must aggregate across the buckets.
- **Apply a composite-key collection**: one-time copy from
  `events_candidate` to `events_composite` with `{tenant_id, user_hash}`.
  Mongos can then balance within a tenant.
- **Reshard onto `user_hash`**: only when no per-tenant locality is needed.

## Post-incident

- Run `make bench-skew SHARD_KEY=fixed RUNS=3` and `make compare-skew`.
- Add the new max/mean to `docs/review.md`.
- File a follow-up if the chosen fix has cross-tenant query implications.
