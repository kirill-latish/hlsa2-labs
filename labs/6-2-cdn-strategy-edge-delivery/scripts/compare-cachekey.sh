#!/usr/bin/env bash
# compare-cachekey - side-by-side hit-ratio + cache-entry cardinality for
# two cache-key conditions (e.g. cachekey-full vs cachekey-stripped). The
# fragmentation story: full-querystring keying collapses the hit ratio and
# explodes cardinality; stripping tracking params recovers both.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"

BEFORE="${BEFORE:?BEFORE=<label> is required (e.g. cachekey-full)}"
AFTER="${AFTER:?AFTER=<label> is required (e.g. cachekey-stripped)}"

BEFORE="${BEFORE}" AFTER="${AFTER}" python3 - "$@" <<'PY'
import os, sys, json
from pathlib import Path
sys.path.insert(0, "scripts")
from analyze_lib import edge_cache_delta, edge_bytes_delta, edge_entries_after, run_dirs, safe_ratio, fmt_pct

def summarize(label):
    base = Path("perf/results")/label
    runs = run_dirs(base)
    if not runs:
        sys.exit(f"ERROR: no runs under {base}")
    status = {}
    byts = {"edge":0.0,"origin":0.0}
    entries = 0.0
    for r in runs:
        d = edge_cache_delta(r)
        for k,v in d.items():
            status[k] = status.get(k,0.0)+v
        b = edge_bytes_delta(r)
        byts["edge"]+=b["edge"]; byts["origin"]+=b["origin"]
        entries = max(entries, edge_entries_after(r))
    total = sum(status.values()) or 1
    tbytes = (byts["edge"]+byts["origin"]) or 1
    return {
        "label": label,
        "hit_req": safe_ratio(status.get("HIT",0), total),
        "hit_bytes": safe_ratio(byts["edge"], tbytes),
        "entries": entries,
        "total": total,
    }

a = summarize(os.environ["BEFORE"])
b = summarize(os.environ["AFTER"])

print(f"# compare-cachekey: {a['label']} -> {b['label']}\n")
print("| metric | before | after |")
print("|--------|-------:|------:|")
print(f"| hit ratio by request | {fmt_pct(a['hit_req'])} | {fmt_pct(b['hit_req'])} |")
print(f"| hit ratio by bytes   | {fmt_pct(a['hit_bytes'])} | {fmt_pct(b['hit_bytes'])} |")
print(f"| cache-entry cardinality | {int(a['entries'])} | {int(b['entries'])} |")
print()
dh = b["hit_req"]-a["hit_req"]
print(f"hit-ratio-by-request delta: {dh*100:+.2f} pts")
print(f"cardinality delta: {int(b['entries']-a['entries']):+d} entries")
print()
print("Mechanism: every cache-key dimension that does NOT change the response")
print("(utm_source/fbclid/gclid) must be stripped, or identical content fragments")
print("into a separate, guaranteed-miss entry per variant. Residual risk: stripping")
print("a param that DOES change the response would serve wrong content - the")
print("allowlist must be correct.")
PY
