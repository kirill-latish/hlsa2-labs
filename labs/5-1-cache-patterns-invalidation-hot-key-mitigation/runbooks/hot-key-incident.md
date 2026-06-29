# Runbook: hot key (one shard saturated, others idle)

> Page trigger: alert `HotShardImbalance`
> (one Redis node taking >2x the average ops/sec for 2m). Cluster-wide
> ops/sec can look completely healthy while one node is on fire.

## 1. Confirm scope (60 s)

- Open the **Cache Overview** dashboard, **Per-node Redis ops/sec**
  panel. Healthy = three lines roughly together. Hot key = one line far
  above the others.
- Do NOT trust the cluster average. 40% of traffic to one of three
  shards still averages ~33% per shard - the average hides the fire.
- `make cluster-status` shows per-node key counts (placement), the panel
  shows per-node *load* - the hot key is about load, not key count.

## 2. Identify the hot key (60 s)

- On the hot node: `docker compose exec redis-<n> redis-cli --hotkeys`
  (or sample `MONITOR` briefly). The lab's injected key is whatever you
  passed to `make inject-hot-key` (e.g. `celebrity-1`).
- Because the app shards by `crc32(key) % 3`, the key deterministically
  maps to one node - moving it requires changing the key, not the node.

## 3. Stop the bleeding (2 min)

- **Apply local LRU**: `make apply-fix CANDIDATE=local-lru
  LOCAL_SIZE=1000 LOCAL_TTL=5s`. The app now serves the hottest keys
  from in-process memory, so the hot key barely touches the shared
  cache. Watch the per-node panel rebalance and the shared
  source-fetch rate drop.
- If a single app process is still saturated, scale out app instances
  (each gets its own local LRU) and/or shorten the local TTL.

## 4. Residual risk of the fix

Local LRU introduces **per-process inconsistency**: for the local TTL
window each app instance may serve a slightly different version of the
hot key. Keep the local TTL short (seconds) and ensure the hot key's
freshness budget tolerates it (see `docs/staleness-policy.md`). If it
does not, prefer key-splitting/replication of the hot key across shards
instead of local caching.

## 5. Postmortem checklist

- [ ] Was the imbalance invisible on cluster-average dashboards? (Add a
      per-node panel + the `HotShardImbalance` alert if missing.)
- [ ] Is this key hot permanently (celebrity) or transiently (event)?
- [ ] Should the hot key be split (`key#1..N`) across shards instead?
- [ ] Does local caching violate any freshness contract?
