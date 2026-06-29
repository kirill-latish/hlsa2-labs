#!/usr/bin/env bash
# corrupt-projection - snapshot the healthy projection (so we can prove
# the rebuild restored it) and then simulate a bug that corrupted the
# read-model: flip rows to a CORRUPT status with negative amounts and
# drop a chunk of rows. Saves the pre-corruption baseline + current
# side-effect count to perf/results/replay/baseline.json.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

OUT="${RESULTS_ROOT}/replay"
mkdir -p "${OUT}"

wait_consumer_drained 30

rows="$(psql_q 'SELECT count(*) FROM projection')"; rows="${rows:-0}"
hash="$(psql_q "SELECT COALESCE(md5(string_agg(order_id, ',' ORDER BY order_id)), '')  FROM projection")"
side_before="$(psql_q 'SELECT count(*) FROM side_effects')"; side_before="${side_before:-0}"

python3 - "$OUT" "$rows" "$hash" "$side_before" <<'PY'
import json, sys
out, rows, h, side = sys.argv[1], int(sys.argv[2]), sys.argv[3], int(sys.argv[4])
open(f"{out}/baseline.json","w").write(json.dumps({
  "projection_rows": rows,
  "projection_order_hash": h,
  "side_effects_before": side,
}, indent=2))
print(json.dumps({"projection_rows": rows, "side_effects_before": side}))
PY

log_step "corrupting the projection (status=CORRUPT, negative amounts, dropping half the rows)"
psql_exec "UPDATE projection SET status='CORRUPT', amount = -1, last_seq = 0;"
psql_exec "DELETE FROM projection WHERE (abs(hashtext(order_id)) % 2) = 0;"

corrupt_rows="$(psql_q 'SELECT count(*) FROM projection')"
corrupt_marks="$(psql_q "SELECT count(*) FROM projection WHERE status='CORRUPT'")"
echo
echo "corrupt-projection: ok. baseline rows=${rows}, now rows=${corrupt_rows} (CORRUPT=${corrupt_marks})."
echo "Next: make replay-rebuild FROM=earliest MODE=rebuild-only LABEL=replay"
