#!/usr/bin/env bash
# cluster-status - the client-side-sharding analogue of CLUSTER INFO.
# Pings all three standalone Redis nodes and prints each node's key
# count (DBSIZE). A balanced warm set lands roughly evenly; a hot key
# shows up as one node carrying disproportionate traffic (see the
# per-node ops/sec panel in Grafana, since DBSIZE counts keys not load).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
cd "${LAB_ROOT}"

NODES=(redis-1 redis-2 redis-3)
all_ok=1
echo "Node      PING     DBSIZE"
echo "--------  -------  -------"
for n in "${NODES[@]}"; do
  ping="$(docker compose exec -T "${n}" redis-cli ping 2>/dev/null | tr -d '\r' || echo 'DOWN')"
  size="$(docker compose exec -T "${n}" redis-cli dbsize 2>/dev/null | tr -d '\r' || echo '?')"
  printf "%-8s  %-7s  %-7s\n" "${n}" "${ping}" "${size}"
  [[ "${ping}" == "PONG" ]] || all_ok=0
done

echo
if [[ "${all_ok}" -eq 1 ]]; then
  echo "CLUSTER_OK: all 3 shards reachable (client-side sharding healthy)."
else
  echo "CLUSTER_DEGRADED: at least one shard is unreachable." >&2
  exit 1
fi
