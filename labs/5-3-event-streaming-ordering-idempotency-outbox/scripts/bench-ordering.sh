#!/usr/bin/env bash
# bench-ordering KEY=wrong|entity - drive the workload with the chosen
# partition key and measure the ordering-violation rate the consumer
# detects via per-entity sequence numbers. KEY=wrong scatters an order's
# events across partitions (out of order); KEY=entity keeps them in one
# partition (in order). Writes perf/results/<LABEL>/summary.json.
#
# Env / make vars:
#   KEY       wrong | entity                 (default wrong)
#   DURATION  run length                     (default 3m)
#   LABEL     perf/results subdir            (default ordering-<KEY>)
#   RATE_EPS  events/s                       (default 200)
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

KEY="${KEY:-wrong}"
DUR="$(to_seconds "${DURATION:-3m}")"
LABEL="${LABEL:-ordering-${KEY}}"
RATE_EPS="${RATE_EPS:-200}"
OUT="${RESULTS_ROOT}/${LABEL}"
mkdir -p "${OUT}"

case "${KEY}" in
  wrong|entity) ;;
  *) echo "KEY must be wrong|entity"; exit 2 ;;
esac

log_step "bench-ordering KEY=${KEY} for ${DUR}s (reset projection for a clean count)"
psql_exec "TRUNCATE projection RESTART IDENTITY;"
set_consumer_mode "idempotent-consumer"
set_replay_mode "off"
producer_config "{\"publish_mode\":\"direct\",\"key_strategy\":\"${KEY}\",\"duplicate_rate\":0}"

viol0="$(promget 'sum(lab53_ordering_violations_total)')"; viol0="${viol0:-0}"
cons0="$(promget 'sum(lab53_events_consumed_total)')"; cons0="${cons0:-0}"

producer_run "${RATE_EPS}" "${DUR}" "${LABEL}"
wait_consumer_drained 90

viol1="$(promget 'sum(lab53_ordering_violations_total)')"; viol1="${viol1:-0}"
cons1="$(promget 'sum(lab53_events_consumed_total)')"; cons1="${cons1:-0}"
curl -fsS "${CONSUMER1}/metrics" >"${OUT}/consumer-metrics.txt" 2>/dev/null || true

python3 - "$OUT" "$LABEL" "$KEY" "$viol0" "$viol1" "$cons0" "$cons1" <<'PY'
import json, sys
out, label, key = sys.argv[1], sys.argv[2], sys.argv[3]
v0,v1,c0,c1 = (float(x) for x in sys.argv[4:8])
viol = max(0.0, v1-v0); cons = max(0.0, c1-c0)
res = {
  "label": label,
  "key_strategy": key,
  "ordering_violations": int(viol),
  "events_consumed": int(cons),
  "ordering_violation_rate": round(viol/cons, 6) if cons else 0.0,
}
open(f"{out}/summary.json","w").write(json.dumps(res, indent=2))
print(json.dumps(res, indent=2))
PY

echo
echo "bench-ordering: wrote ${OUT}/summary.json"
