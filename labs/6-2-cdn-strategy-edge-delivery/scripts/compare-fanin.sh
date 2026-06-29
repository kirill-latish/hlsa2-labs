#!/usr/bin/env bash
# compare-fanin - side-by-side origin fan-in for the popular-object expiry
# with shielding off vs on. Shows the herd (fan-in ~ PoP count) collapsing
# to ~one origin fetch once the shield is in the path.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"

BEFORE="${BEFORE:?BEFORE=<label> is required (e.g. shield-off)}"
AFTER="${AFTER:?AFTER=<label> is required (e.g. shield-on)}"

BEFORE="${BEFORE}" AFTER="${AFTER}" python3 - <<'PY'
import os, sys, json
from pathlib import Path
sys.path.insert(0, "scripts")
from analyze_lib import origin_object_delta, origin_requests_delta

def summarize(label):
    base = Path("perf/results")/label
    if not (base/"origin-metrics-after.txt").exists():
        sys.exit(f"ERROR: no origin snapshots under {base}")
    meta = json.loads((base/"meta.json").read_text()) if (base/"meta.json").exists() else {}
    obj = meta.get("object","s0"); pops = meta.get("pops",3)
    return {"label":label, "obj":obj, "pops":pops,
            "fanin":origin_object_delta(base,obj), "total":origin_requests_delta(base)}

a = summarize(os.environ["BEFORE"])
b = summarize(os.environ["AFTER"])
print(f"# compare-fanin: {a['label']} -> {b['label']}\n")
print("| metric | before | after |")
print("|--------|-------:|------:|")
print(f"| origin fan-in (object {a['obj']}) | {int(a['fanin'])} | {int(b['fanin'])} |")
print(f"| total origin requests in burst | {int(a['total'])} | {int(b['total'])} |")
print()
print(f"fan-in reduction: {int(a['fanin'])} -> {int(b['fanin'])} "
      f"({int(a['fanin']-b['fanin']):+d})")
print()
print("Mechanism: request collapsing (singleflight WITHIN a PoP) bounds each PoP")
print("to one in-flight upstream fetch; origin shielding (the shared mid-tier cache")
print("ACROSS PoPs) collapses the PoPs' misses into ~one origin fetch. Cost: a hop")
print("for cold content.")
PY
