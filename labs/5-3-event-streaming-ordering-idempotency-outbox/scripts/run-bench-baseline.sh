#!/usr/bin/env bash
# Baseline pipeline benchmark. Drives a representative event workload
# (direct publish, low duplicate injection) for RUNS runs and snapshots,
# per run: throughput (events/s), consumer lag count + time, and the
# duplicate rate. Mirrors lab 3-3's run-bench-baseline structure.
#
# Env / make vars:
#   RUNS      number of runs        (default 3)
#   DURATION  per-run duration      (default 5m; accepts 5m/180s/180)
#   LABEL     perf/results subdir   (default baseline)
#   RATE      events/s              (default 200)
#   DUPRATE   duplicate injection   (default 0.02 -> low-but-nonzero)
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

RUNS="${RUNS:-3}"
LABEL="${LABEL:-baseline}"
RATE="${RATE:-200}"
DUPRATE="${DUPRATE:-0.02}"
DUR="$(to_seconds "${DURATION:-5m}")"

OUT_BASE="${RESULTS_ROOT}/${LABEL}"
mkdir -p "${OUT_BASE}"

log_step "baseline: idempotent consumers, direct publish, dup_rate=${DUPRATE}"
set_consumer_mode "idempotent-consumer"
set_replay_mode "off"
producer_config "{\"publish_mode\":\"direct\",\"key_strategy\":\"entity\",\"duplicate_rate\":${DUPRATE}}"

for i in $(seq 1 "${RUNS}"); do
  RUN_DIR="${OUT_BASE}/run${i}"
  mkdir -p "${RUN_DIR}"
  echo
  echo "================================================================="
  echo "[baseline] LABEL=${LABEL} run=${i}/${RUNS} rate=${RATE} duration=${DUR}s"
  echo "================================================================="

  consumed0="$(promget 'sum(lab53_events_consumed_total)')"; consumed0="${consumed0:-0}"
  suppressed0="$(promget 'sum(lab53_duplicate_suppressed_total)')"; suppressed0="${suppressed0:-0}"
  produced0="$(promget 'sum(lab53_events_produced_total)')"; produced0="${produced0:-0}"

  producer_run "${RATE}" "${DUR}" "${LABEL}-run${i}"
  wait_consumer_drained 90

  consumed1="$(promget 'sum(lab53_events_consumed_total)')"; consumed1="${consumed1:-0}"
  suppressed1="$(promget 'sum(lab53_duplicate_suppressed_total)')"; suppressed1="${suppressed1:-0}"
  produced1="$(promget 'sum(lab53_events_produced_total)')"; produced1="${produced1:-0}"
  lag_peak="$(promget "max_over_time(lab53:consumer_lag_count[${DUR}s])")"; lag_peak="${lag_peak:-0}"
  lag_age_peak="$(promget "max_over_time(lab53:consumer_lag_age_seconds[${DUR}s])")"; lag_age_peak="${lag_age_peak:-0}"

  curl -fsS "${CONSUMER1}/metrics" >"${RUN_DIR}/consumer-metrics.txt" 2>/dev/null || true

  python3 - "$RUN_DIR" "$i" "$DUR" "$consumed0" "$consumed1" "$suppressed0" "$suppressed1" \
            "$produced0" "$produced1" "$lag_peak" "$lag_age_peak" <<'PY'
import json, sys
run_dir, run, dur = sys.argv[1], int(sys.argv[2]), float(sys.argv[3])
c0,c1,s0,s1,p0,p1,lag,lagage = map(float, sys.argv[4:12])
consumed = max(0.0, c1-c0); suppressed = max(0.0, s1-s0); produced = max(0.0, p1-p0)
out = {
  "run": run,
  "duration_s": dur,
  "throughput_eps": round(consumed/dur, 3) if dur else 0,
  "events_produced": produced,
  "events_consumed": consumed,
  "duplicates_suppressed": suppressed,
  "duplicate_rate": round(suppressed/consumed, 6) if consumed else 0.0,
  "lag_count_peak": lag,
  "lag_time_peak_s": lagage,
}
open(f"{run_dir}/summary.json","w").write(json.dumps(out, indent=2))
print(json.dumps(out))
PY
  echo "[baseline] wrote ${RUN_DIR}/summary.json"
done

echo
echo "Done. Next: make analyze-baseline LABEL=${LABEL}"
