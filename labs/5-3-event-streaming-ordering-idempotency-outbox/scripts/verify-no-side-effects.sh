#!/usr/bin/env bash
# verify-no-side-effects LABEL=<...> - prove the rebuild-only replay did
# NOT re-fire external side effects: the side_effects count after the
# replay must equal the count captured just before it. (A reprocess-mode
# replay WOULD increase it - that is the contrast the lab draws.) Writes
# perf/results/<LABEL>/no-side-effects.json.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

LABEL="${LABEL:-replay}"
OUT="${RESULTS_ROOT}/${LABEL}"
mkdir -p "${OUT}"

before="$(cat "${OUT}/side-effects-before.txt" 2>/dev/null || echo 0)"
after="$(psql_q 'SELECT count(*) FROM side_effects')"; after="${after:-0}"
mode="$(python3 -c "import json;print(json.load(open('${OUT}/replay.json'))['mode'])" 2>/dev/null || echo unknown)"

python3 - "$OUT" "$LABEL" "$mode" "$before" "$after" <<'PY'
import json, sys
out, label, mode = sys.argv[1], sys.argv[2], sys.argv[3]
before, after = int(sys.argv[4]), int(sys.argv[5])
delta = after - before
if mode == "rebuild-only":
    ok = (delta == 0)
    verdict = "PASS: rebuild-only fired no new external side effects." if ok \
              else f"FAIL: side effects increased by {delta} during rebuild-only."
else:
    ok = (delta > 0)
    verdict = f"reprocess re-fired {delta} side effects (expected in reprocess mode)."
res = {
  "label": label, "mode": mode,
  "side_effects_before": before, "side_effects_after": after,
  "delta": delta, "no_new_side_effects": (delta == 0), "ok": ok, "verdict": verdict,
}
open(f"{out}/no-side-effects.json","w").write(json.dumps(res, indent=2))
print(json.dumps(res, indent=2))
PY

echo
echo "verify-no-side-effects: wrote ${OUT}/no-side-effects.json"
