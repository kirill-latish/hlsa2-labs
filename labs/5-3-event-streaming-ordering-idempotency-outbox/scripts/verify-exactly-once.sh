#!/usr/bin/env bash
# verify-exactly-once LABEL=<...> - count external side effects vs the
# number of UNIQUE business events that caused them. Exactly-once EFFECT
# means side_effects == unique_events. A naive consumer overshoots
# (duplicates + redelivery double-applied); an idempotent consumer hits
# the count exactly. Writes perf/results/<LABEL>/result.json.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

LABEL="${LABEL:-idempotency}"
OUT="${RESULTS_ROOT}/${LABEL}"
mkdir -p "${OUT}"

wait_consumer_drained 60

side_total="$(psql_q 'SELECT count(*) FROM side_effects')"; side_total="${side_total:-0}"
side_unique="$(psql_q 'SELECT count(DISTINCT event_id) FROM side_effects')"; side_unique="${side_unique:-0}"
proj_rows="$(psql_q 'SELECT count(*) FROM projection')"; proj_rows="${proj_rows:-0}"
dedup_rows="$(psql_q 'SELECT count(*) FROM processed_ids')"; dedup_rows="${dedup_rows:-0}"
suppressed="$(promget 'sum(lab53_duplicate_suppressed_total)')"; suppressed="${suppressed:-0}"

python3 - "$OUT" "$LABEL" "$side_total" "$side_unique" "$proj_rows" "$dedup_rows" "$suppressed" <<'PY'
import json, sys
out, label = sys.argv[1], sys.argv[2]
side_total, side_unique, proj, dedup, suppressed = (float(x) for x in sys.argv[3:8])
ratio = (side_total / side_unique) if side_unique else 0.0
res = {
  "label": label,
  "side_effects_total": int(side_total),
  "unique_events": int(side_unique),
  "side_effect_ratio": round(ratio, 4),
  "exactly_once_effect": int(side_total) == int(side_unique) and side_unique > 0,
  "projection_rows": int(proj),
  "dedup_table_rows": int(dedup),
  "duplicates_suppressed_total": int(suppressed),
}
open(f"{out}/result.json","w").write(json.dumps(res, indent=2))
print(json.dumps(res, indent=2))
PY

echo
echo "verify-exactly-once: wrote ${OUT}/result.json"
