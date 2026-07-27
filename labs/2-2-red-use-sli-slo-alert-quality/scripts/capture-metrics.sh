#!/usr/bin/env bash
# Snapshot SLO evidence out of Prometheus into artifacts/.
#
# run-experiment.sh saves the load generator's stdout, which proves what
# load was *offered*. It says nothing about what the SLO pipeline actually
# observed. This script captures the served side: SLI ratios, burn rates,
# alert states, and request mix. Prometheus retention here is 2 days
# (docker-compose.yml), so evidence not captured is evidence lost.
#
# Usage:
#   capture-metrics.sh snapshot <out_dir> <tag>
#       Instant snapshot of every SLI/burn-rate series plus alert state.
#       Run once before an experiment and once after.
#
#   capture-metrics.sh range <out_dir> <tag> <start_epoch> <end_epoch>
#       Range series over the experiment window. This is what TTD/TTR are
#       read from: the ALERTS metric records exactly when each alert
#       entered firing state, so detect/reset latency is measured, not
#       estimated.
#
#   capture-metrics.sh cardinality <out_dir>
#       TSDB series counts for the label cardinality audit.
#
# All output is JSON so it can be re-read later without re-querying a
# Prometheus whose retention window has since rolled over.

set -euo pipefail

PROM="${PROM_URL:-http://localhost:9090}"
MODE="${1:-}"

# Every window the burn-rate alerts depend on.
WINDOWS=(5m 30m 1h 6h)

_q() {
  # Instant query -> raw JSON on stdout.
  curl -s -G "${PROM}/api/v1/query" --data-urlencode "query=$1"
}

_qr() {
  # Range query -> raw JSON on stdout. $2=start $3=end $4=step
  curl -s -G "${PROM}/api/v1/query_range" \
    --data-urlencode "query=$1" \
    --data-urlencode "start=$2" \
    --data-urlencode "end=$3" \
    --data-urlencode "step=${4:-15s}"
}

_die() { echo "capture-metrics: $*" >&2; exit 2; }

case "${MODE}" in

  snapshot)
    OUT_DIR="${2:?out_dir required}"
    TAG="${3:?tag required}"
    mkdir -p "${OUT_DIR}"
    FILE="${OUT_DIR}/${TAG}.snapshot.json"

    {
      echo "{"
      echo "  \"captured_at\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\","
      echo "  \"captured_epoch\": $(date -u +%s),"
      echo "  \"tag\": \"${TAG}\","

      # --- SLI ratios and burn rates, both SLOs, every window ----------
      echo "  \"sli\": {"
      first=1
      for slo in availability latency; do
        for w in "${WINDOWS[@]}"; do
          [ $first -eq 0 ] && echo ","
          first=0
          printf '    "%s_ratio_rate%s": %s' "${slo}" "${w}" \
            "$(_q "sli:checkout_${slo}:ratio_rate${w}" \
               | python3 -c 'import json,sys;r=json.load(sys.stdin)["data"]["result"];v=r[0]["value"][1] if r else "null";print("null" if v in ("NaN","+Inf","-Inf") else v)')"
        done
      done
      echo ""
      echo "  },"

      echo "  \"burn_rate\": {"
      first=1
      for slo in availability latency; do
        for w in "${WINDOWS[@]}"; do
          [ $first -eq 0 ] && echo ","
          first=0
          printf '    "%s_burnrate%s": %s' "${slo}" "${w}" \
            "$(_q "slo:checkout_${slo}:burnrate${w}" \
               | python3 -c 'import json,sys;r=json.load(sys.stdin)["data"]["result"];v=r[0]["value"][1] if r else "null";print("null" if v in ("NaN","+Inf","-Inf") else v)')"
        done
      done
      echo ""
      echo "  },"

      # --- request mix: proves what the SLI denominator was made of ----
      printf '  "requests_by_status_class_5m": '
      _q 'sum by (status_class) (increase(http_requests_total{service="checkout",route="/checkout"}[5m]))' \
        | python3 -c '
import json,sys
r=json.load(sys.stdin)["data"]["result"]
print(json.dumps({s["metric"].get("status_class","?"): round(float(s["value"][1]),2) for s in r}))'
      echo ","

      # --- observed latency distribution -------------------------------
      printf '  "latency_quantiles_5m": '
      python3 -c '
import json,subprocess,sys
out={}
for p in (0.5,0.95,0.99):
    q=f"histogram_quantile({p}, sum by (le) (rate(http_request_duration_seconds_bucket{{service=\"checkout\",route=\"/checkout\"}}[5m])))"
    j=json.loads(subprocess.run(["curl","-s","-G","'"${PROM}"'/api/v1/query","--data-urlencode",f"query={q}"],capture_output=True,text=True).stdout)
    r=j["data"]["result"]
    out[f"p{int(p*100)}"]=round(float(r[0]["value"][1]),4) if r and r[0]["value"][1] not in ("NaN",) else None
print(json.dumps(out))'
      echo ","

      # --- alert state at capture time ---------------------------------
      printf '  "alerts": '
      curl -s "${PROM}/api/v1/alerts" | python3 -c '
import json,sys
d=json.load(sys.stdin)["data"]["alerts"]
print(json.dumps([{
  "alertname": a["labels"].get("alertname"),
  "slo": a["labels"].get("slo"),
  "severity": a["labels"].get("severity"),
  "state": a.get("state"),
  "activeAt": a.get("activeAt"),
  "value": a.get("value"),
} for a in d], indent=2))'
      echo ","

      # --- rule health: catches a rule that silently fails to evaluate -
      printf '  "rule_health": '
      curl -s "${PROM}/api/v1/rules" | python3 -c '
import json,sys
g=json.load(sys.stdin)["data"]["groups"]
print(json.dumps([{
  "group": grp["name"],
  "rule": r.get("name"),
  "type": r.get("type"),
  "health": r.get("health"),
  "state": r.get("state"),
  "lastError": r.get("lastError",""),
} for grp in g for r in grp["rules"]], indent=2))'

      echo "}"
    } > "${FILE}"

    python3 -m json.tool "${FILE}" > /dev/null \
      || _die "produced invalid JSON at ${FILE}"
    echo "[capture] snapshot -> ${FILE}"
    ;;

  range)
    OUT_DIR="${2:?out_dir required}"
    TAG="${3:?tag required}"
    START="${4:?start_epoch required}"
    END="${5:?end_epoch required}"
    mkdir -p "${OUT_DIR}"
    FILE="${OUT_DIR}/${TAG}.range.json"

    # Pad the window so the alert's post-injection decay is visible: TTR
    # cannot be read from a range that ends the instant load stops.
    #
    # 40 minutes, not 20. These alerts resolve when the *short* window
    # empties, and the short window on the slow-burn pair is 30m - so a
    # 20-minute pad ends before the alert has resolved and TTR reads as
    # "never". Prometheus retains 2 days, so a range can always be
    # re-captured later over a wider window if this still proves short.
    PAD="${RANGE_PAD:-2400}"
    RSTART=$((START - 120))
    REND=$((END + PAD))

    # Prometheus cannot return samples that do not exist yet. run-experiment.sh
    # calls this only RANGE_WAIT (default 300s) after the run ends, so a 2400s
    # pad would silently request 35 minutes of future time and return a series
    # that merely stops - indistinguishable from an alert that resolved.
    # Clamp to now and say so; re-run this mode later to pick up the rest
    # (retention is 2 days, so the data is still there).
    NOW="$(date -u +%s)"
    if [ "${REND}" -gt "${NOW}" ]; then
      echo "[capture] NOTE: requested end $(( (REND - NOW) / 60 ))m in the future;" \
           "clamping to now. Alert decay after $(date -u -r "${NOW}" +%H:%M:%SZ 2>/dev/null || date -u +%H:%M:%SZ)" \
           "is NOT in this file - re-run 'capture-metrics.sh range' later for full TTR." >&2
      REND="${NOW}"
    fi

    python3 - "$FILE" "$RSTART" "$REND" "$PROM" <<'PY'
import json, subprocess, sys, urllib.parse

out_file, start, end, prom = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]

def qr(expr, step="15s"):
    args = ["curl", "-s", "-G", f"{prom}/api/v1/query_range",
            "--data-urlencode", f"query={expr}",
            "--data-urlencode", f"start={start}",
            "--data-urlencode", f"end={end}",
            "--data-urlencode", f"step={step}"]
    j = json.loads(subprocess.run(args, capture_output=True, text=True).stdout)
    if j.get("status") != "success":
        return {"error": j.get("error", "query failed")}
    return {
        s.get("metric", {}).get("__name__") or json.dumps(s.get("metric", {})): s["values"]
        for s in j["data"]["result"]
    }

series = {}
for slo in ("availability", "latency"):
    for w in ("5m", "30m", "1h", "6h"):
        series[f"sli:checkout_{slo}:ratio_rate{w}"] = qr(f"sli:checkout_{slo}:ratio_rate{w}")
        series[f"slo:checkout_{slo}:burnrate{w}"] = qr(f"slo:checkout_{slo}:burnrate{w}")

# ALERTS is the ground truth for TTD/TTR: it exists only while an alert is
# pending or firing, so the first/last timestamp of the firing series is
# the detect and reset moment.
alerts = {}
j = json.loads(subprocess.run(
    ["curl", "-s", "-G", f"{prom}/api/v1/query_range",
     "--data-urlencode", "query=ALERTS{alertname=~\"SLO.*\"}",
     "--data-urlencode", f"start={start}",
     "--data-urlencode", f"end={end}",
     "--data-urlencode", "step=15s"],
    capture_output=True, text=True).stdout)
if j.get("status") == "success":
    for s in j["data"]["result"]:
        m = s["metric"]
        key = f'{m.get("alertname")}/{m.get("alertstate")}'
        alerts[key] = {
            "first_ts": s["values"][0][0],
            "last_ts": s["values"][-1][0],
            "samples": len(s["values"]),
            "values": s["values"],
        }

# Request mix over the window, so the review can state the actual
# denominator rather than the offered load.
mix = qr('sum by (status_class) (rate(http_requests_total'
         '{service="checkout",route="/checkout"}[5m]))')

with open(out_file, "w") as f:
    json.dump({
        "range_start_epoch": int(start),
        "range_end_epoch": int(end),
        "step": "15s",
        "note": "range padded -120s/+1200s around the run so alert decay (TTR) is visible",
        "series": series,
        "alerts_firing_windows": alerts,
        "request_mix_by_status_class": mix,
    }, f, indent=2)
print(f"[capture] range -> {out_file}")
PY
    ;;

  cardinality)
    OUT_DIR="${2:?out_dir required}"
    mkdir -p "${OUT_DIR}"
    FILE="${OUT_DIR}/cardinality.json"

    {
      echo "{"
      echo "  \"captured_at\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\","
      printf '  "tsdb_status": '
      curl -s "${PROM}/api/v1/status/tsdb" \
        | python3 -c 'import json,sys;print(json.dumps(json.load(sys.stdin)["data"],indent=2))'
      echo ","
      printf '  "series_per_http_metric": '
      _q 'count by (__name__) ({__name__=~"http_.*"})' | python3 -c '
import json,sys
r=json.load(sys.stdin)["data"]["result"]
print(json.dumps({s["metric"]["__name__"]: int(float(s["value"][1])) for s in r}, indent=2))'
      echo ","
      printf '  "distinct_label_values": '
      python3 -c '
import json,subprocess
out={}
for metric,label in (("http_requests_total","route"),("http_requests_total","method"),
                     ("http_requests_total","status_class"),
                     ("http_request_duration_seconds_bucket","route"),
                     ("http_request_duration_seconds_bucket","method"),
                     ("http_request_duration_seconds_bucket","le")):
    q=f"count(count by ({label}) ({metric}{{service=\"checkout\"}}))"
    j=json.loads(subprocess.run(["curl","-s","-G","'"${PROM}"'/api/v1/query",
        "--data-urlencode",f"query={q}"],capture_output=True,text=True).stdout)
    r=j["data"]["result"]
    out[f"{metric}.{label}"]=int(float(r[0]["value"][1])) if r else 0
print(json.dumps(out,indent=2))'
      echo "}"
    } > "${FILE}"

    python3 -m json.tool "${FILE}" > /dev/null || _die "invalid JSON at ${FILE}"
    echo "[capture] cardinality -> ${FILE}"
    ;;

  *)
    _die "usage: $0 {snapshot <dir> <tag>|range <dir> <tag> <start> <end>|cardinality <dir>}"
    ;;
esac
