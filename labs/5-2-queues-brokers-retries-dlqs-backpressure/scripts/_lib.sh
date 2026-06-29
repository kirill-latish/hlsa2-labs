#!/usr/bin/env bash
# Shared helpers for the bench/inject scripts. Source me with:
#   source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

# to_seconds <value> - accept "5m", "300s", "90", "10m" -> integer seconds.
to_seconds() {
  local v="${1:-0}"
  case "${v}" in
    *m) echo $(( ${v%m} * 60 )) ;;
    *s) echo "${v%s}" ;;
    *)  echo "${v}" ;;
  esac
}

# pct_to_frac <value> - accept "10pct", "10%", "0.10" -> decimal fraction.
pct_to_frac() {
  local v="${1:-0}"
  case "${v}" in
    *pct) awk -v x="${v%pct}" 'BEGIN{printf "%.4f", x/100}' ;;
    *%)   awk -v x="${v%\%}" 'BEGIN{printf "%.4f", x/100}' ;;
    *)    echo "${v}" ;;
  esac
}

# rate_to_rps <value> <capacity> - accept "2x" -> capacity*2, or plain rps.
rate_to_rps() {
  local v="${1:-1x}" cap="${2:-300}"
  case "${v}" in
    *x) awk -v m="${v%x}" -v c="${cap}" 'BEGIN{printf "%d", m*c}' ;;
    *)  echo "${v}" ;;
  esac
}

# poll_until_done <loadgen-url> <max_wait_s> - block until loadgen
# reports running=false, or max_wait_s+45 elapses.
poll_until_done() {
  local lg="$1"
  local max="${2:-300}"
  local started end
  started="$(date +%s)"
  end=$(( started + max + 45 ))
  while true; do
    local state running
    state="$(curl -fsS "${lg}/state" 2>/dev/null || echo '{"running":false}')"
    running="$(echo "${state}" | jq -r '.running // false')"
    if [[ "${running}" != "true" ]]; then
      echo "[poll] loadgen completed."
      return 0
    fi
    if [[ "$(date +%s)" -gt "${end}" ]]; then
      echo "[poll] loadgen still running after ${max}s + grace; stopping it."
      curl -fsS -X POST "${lg}/stop" >/dev/null || true
      return 1
    fi
    sleep 5
  done
}

# snapshot_metrics <out_file> - concatenate the producer + 3 consumer
# /metrics endpoints into one Prometheus text file the analyzers parse.
snapshot_metrics() {
  local out="$1"
  : >"${out}"
  curl -fsS "http://localhost:${LAB_PRODUCER_PORT:-8080}/metrics" >>"${out}" 2>/dev/null || true
  curl -fsS "http://localhost:${LAB_CONSUMER1_PORT:-8101}/metrics" >>"${out}" 2>/dev/null || true
  curl -fsS "http://localhost:${LAB_CONSUMER2_PORT:-8102}/metrics" >>"${out}" 2>/dev/null || true
  curl -fsS "http://localhost:${LAB_CONSUMER3_PORT:-8103}/metrics" >>"${out}" 2>/dev/null || true
}

# drive_run <run_dir> <label> <rate> <duration_s> <poison> <transient_frac>
#           <permanent_frac> <overload_mult> <backpressure:true|false>
# Starts a loadgen run, polls to completion, then snapshots summary.json,
# metrics.txt, and meta.json into <run_dir>.
drive_run() {
  local run_dir="$1" label="$2" rate="$3" dur="$4"
  local poison="${5:-0}" transient="${6:-0}" permanent="${7:-0}"
  local overload="${8:-1}" backpressure="${9:-false}"
  local loadgen="http://localhost:${LAB_LOADGEN_PORT:-8090}"

  mkdir -p "${run_dir}"
  echo "[drive] label=${label} rate=${rate} dur=${dur}s poison=${poison} transient=${transient} permanent=${permanent} overload=${overload}x backpressure=${backpressure}"

  # Snapshot cumulative counters BEFORE the run so analyzers can compute
  # per-run deltas (the Prometheus counters are process-lifetime).
  snapshot_metrics "${run_dir}/metrics-before.txt"

  curl -fsS -X POST -H 'content-type: application/json' \
    -d "$(jq -n \
            --argjson r "${rate}" --argjson d "${dur}" --arg l "${label}" \
            --argjson p "${poison}" --argjson tr "${transient}" --argjson pr "${permanent}" \
            --argjson om "${overload}" --argjson bp "${backpressure}" \
            '{rate:$r,duration_s:$d,label:$l,poison_count:$p,transient_rate:$tr,permanent_rate:$pr,overload_multiplier:$om,backpressure:$bp}')" \
    "${loadgen}/start" >/dev/null

  poll_until_done "${loadgen}" "${dur}"

  curl -fsS "${loadgen}/summary" >"${run_dir}/summary.json" 2>/dev/null || echo '{}' >"${run_dir}/summary.json"
  snapshot_metrics "${run_dir}/metrics.txt"
  jq -n \
    --arg label "${label}" \
    --argjson rate "${rate}" \
    --argjson dur "${dur}" \
    --argjson poison "${poison}" \
    --argjson transient "${transient}" \
    --argjson permanent "${permanent}" \
    --argjson overload "${overload}" \
    --argjson backpressure "${backpressure}" \
    --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{label:$label, rate_rps:$rate, duration_s:$dur, poison_count:$poison,
      transient_rate:$transient, permanent_rate:$permanent,
      overload_multiplier:$overload, backpressure:$backpressure, captured_at:$captured_at}' \
    >"${run_dir}/meta.json"
  echo "[drive] wrote ${run_dir}/"
}

# load_env - source .env if present so LAB_*_PORT overrides apply.
load_env() {
  if [[ -f .env ]]; then
    # shellcheck disable=SC1091
    set -a; source .env; set +a
  fi
}
