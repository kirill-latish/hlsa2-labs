#!/usr/bin/env bash
# analyze-consistency LABEL=<...> - count orphaned state changes: orders
# that committed to the business table but whose event never reached the
# projection (the downstream read model). naive-dualwrite leaves orphans
# after the crash; outbox leaves zero. Writes perf/results/<LABEL>/result.json.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

LABEL="${LABEL:-dualwrite}"
OUT="${RESULTS_ROOT}/${LABEL}"
mkdir -p "${OUT}"

wait_outbox_drained 30
wait_consumer_drained 30

orders="$(psql_q 'SELECT count(*) FROM orders')"; orders="${orders:-0}"
projected="$(psql_q 'SELECT count(*) FROM projection')"; projected="${projected:-0}"
orphans="$(psql_q 'SELECT count(*) FROM orders o WHERE NOT EXISTS (SELECT 1 FROM projection p WHERE p.order_id = o.order_id)')"; orphans="${orphans:-0}"
backlog="$(psql_q 'SELECT count(*) FROM events_outbox WHERE published_at IS NULL')"; backlog="${backlog:-0}"

python3 - "$OUT" "$LABEL" "$orders" "$projected" "$orphans" "$backlog" <<'PY'
import json, sys
out, label = sys.argv[1], sys.argv[2]
orders, projected, orphans, backlog = (int(x) for x in sys.argv[3:7])
res = {
  "label": label,
  "orders_written": orders,
  "projection_orders": projected,
  "orphaned_state_changes": orphans,
  "outbox_backlog": backlog,
  "consistent": orphans == 0,
}
open(f"{out}/result.json","w").write(json.dumps(res, indent=2))
print(json.dumps(res, indent=2))
PY

echo
echo "analyze-consistency: wrote ${OUT}/result.json"
