#!/usr/bin/env bash
# replay-rebuild FROM=earliest MODE=rebuild-only - rewind the consumer
# group to the start of the log and replay it to rebuild the projection.
#
#   MODE=rebuild-only  applies events to the projection but SUPPRESSES
#                      external side effects (the recovery path).
#   MODE=reprocess     re-fires side effects too (the footgun) - use a
#                      sandbox only.
#
# The rebuild duration (start of replay -> projection back to its
# pre-corruption row count) is the recovery-time metric. Writes
# perf/results/<LABEL>/replay.json.
#
# Env / make vars:
#   FROM   earliest | latest        (default earliest)
#   MODE   rebuild-only | reprocess (default rebuild-only)
#   LABEL  perf/results subdir      (default replay)
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

FROM="${FROM:-earliest}"
MODE="${MODE:-rebuild-only}"
LABEL="${LABEL:-replay}"
OUT="${RESULTS_ROOT}/${LABEL}"
mkdir -p "${OUT}"

case "${MODE}" in
  rebuild-only|reprocess) ;;
  *) echo "MODE must be rebuild-only|reprocess"; exit 2 ;;
esac

BASELINE="${RESULTS_ROOT}/replay/baseline.json"
target="$(python3 -c "import json;print(json.load(open('${BASELINE}'))['projection_rows'])" 2>/dev/null || echo 0)"
side_before="$(psql_q 'SELECT count(*) FROM side_effects')"; side_before="${side_before:-0}"
echo "${side_before}" >"${OUT}/side-effects-before.txt"

log_step "replay-rebuild FROM=${FROM} MODE=${MODE} (target projection rows=${target})"
psql_exec "TRUNCATE projection RESTART IDENTITY;"

log_step "restarting consumers in REPLAY_MODE=${MODE}"
docker compose stop consumer-1 consumer-2 consumer-3 >/dev/null 2>&1 || true
if [[ "${FROM}" == "earliest" ]]; then
  rpk group delete "${GROUP}" >/dev/null 2>&1 || true
fi
REPLAY_MODE="${MODE}" docker compose up -d --force-recreate consumer-1 consumer-2 consumer-3 >/dev/null
for c in "${CONSUMERS[@]}"; do
  for _ in $(seq 1 40); do curl -fsS "${c}/healthz" >/dev/null 2>&1 && break; sleep 1; done
done

log_step "replaying; timing rebuild to ${target} projection rows"
start_ns="$(date +%s)"
deadline=$(( start_ns + 180 ))
final=0
while true; do
  final="$(psql_q 'SELECT count(*) FROM projection')"; final="${final:-0}"
  if [[ "${target}" -gt 0 && "${final}" -ge "${target}" ]]; then break; fi
  [[ "$(date +%s)" -gt "${deadline}" ]] && break
  sleep 2
done
end_ns="$(date +%s)"
dur=$(( end_ns - start_ns ))

side_after="$(psql_q 'SELECT count(*) FROM side_effects')"; side_after="${side_after:-0}"
echo "${side_after}" >"${OUT}/side-effects-after.txt"

python3 - "$OUT" "$FROM" "$MODE" "$dur" "$target" "$final" "$side_before" "$side_after" <<'PY'
import json, sys
out, frm, mode, dur, target, final, sb, sa = sys.argv[1:9]
open(f"{out}/replay.json","w").write(json.dumps({
  "from": frm, "mode": mode,
  "rebuild_duration_s": int(dur),
  "target_rows": int(target),
  "final_rows": int(final),
  "side_effects_before": int(sb),
  "side_effects_after": int(sa),
}, indent=2))
print(json.dumps({"mode":mode,"rebuild_duration_s":int(dur),"final_rows":int(final),"target_rows":int(target)}))
PY

log_step "restoring consumers to normal (REPLAY_MODE=off)"
docker compose stop consumer-1 consumer-2 consumer-3 >/dev/null 2>&1 || true
REPLAY_MODE=off docker compose up -d --force-recreate consumer-1 consumer-2 consumer-3 >/dev/null

echo
echo "replay-rebuild: wrote ${OUT}/replay.json (rebuild ~${dur}s)"
