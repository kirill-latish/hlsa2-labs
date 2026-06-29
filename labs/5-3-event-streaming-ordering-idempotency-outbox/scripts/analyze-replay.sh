#!/usr/bin/env bash
# analyze-replay LABEL=<...> - confirm the projection was rebuilt
# correctly from the log: row count back to the pre-corruption baseline,
# no CORRUPT markers, no negative amounts. Reports the rebuild duration
# (recovery-time metric). Writes perf/results/<LABEL>/analysis.json.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

LABEL="${LABEL:-replay}"
OUT="${RESULTS_ROOT}/${LABEL}"
mkdir -p "${OUT}"

BASELINE="${RESULTS_ROOT}/replay/baseline.json"
base_rows="$(python3 -c "import json;print(json.load(open('${BASELINE}'))['projection_rows'])" 2>/dev/null || echo 0)"
base_hash="$(python3 -c "import json;print(json.load(open('${BASELINE}'))['projection_order_hash'])" 2>/dev/null || echo '')"
dur="$(python3 -c "import json;print(json.load(open('${OUT}/replay.json'))['rebuild_duration_s'])" 2>/dev/null || echo 0)"

now_rows="$(psql_q 'SELECT count(*) FROM projection')"; now_rows="${now_rows:-0}"
now_hash="$(psql_q "SELECT COALESCE(md5(string_agg(order_id, ',' ORDER BY order_id)), '') FROM projection")"
corrupt="$(psql_q "SELECT count(*) FROM projection WHERE status='CORRUPT'")"; corrupt="${corrupt:-0}"
negative="$(psql_q "SELECT count(*) FROM projection WHERE amount < 0")"; negative="${negative:-0}"

python3 - "$OUT" "$LABEL" "$base_rows" "$now_rows" "$base_hash" "$now_hash" "$corrupt" "$negative" "$dur" <<'PY'
import json, sys
out, label = sys.argv[1], sys.argv[2]
base_rows, now_rows = int(sys.argv[3]), int(sys.argv[4])
base_hash, now_hash = sys.argv[5], sys.argv[6]
corrupt, negative, dur = int(sys.argv[7]), int(sys.argv[8]), int(sys.argv[9])
rebuilt = (now_rows >= base_rows and base_rows > 0 and corrupt == 0 and negative == 0)
res = {
  "label": label,
  "rebuild_duration_s": dur,
  "baseline_rows": base_rows,
  "rebuilt_rows": now_rows,
  "order_hash_matches_baseline": (base_hash == now_hash and base_hash != ""),
  "corrupt_rows_remaining": corrupt,
  "negative_amount_rows": negative,
  "projection_rebuilt": rebuilt,
}
open(f"{out}/analysis.json","w").write(json.dumps(res, indent=2))
print(json.dumps(res, indent=2))
PY

echo
echo "analyze-replay: wrote ${OUT}/analysis.json"
