#!/usr/bin/env python3
"""analyze-fanin.py - report the origin fan-in for the popular object on
its expiry (the distributed thundering herd). Reads the origin
before/after snapshots written by expire-popular-object.sh and computes
how many fetches the origin received for the hot object:

  shield off -> fan-in ~ number of PoPs (each PoP fetched independently)
  shield on  -> fan-in ~ 1 (the shield collapsed them)

Usage: python3 scripts/analyze-fanin.py perf/results/shield-off
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from analyze_lib import origin_object_delta, origin_requests_delta  # noqa: E402

POPULAR_OBJECT = "s0"


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    if not (base / "origin-metrics-after.txt").exists():
        print(f"ERROR: no origin snapshots under {base} (run make expire-popular-object first)", file=sys.stderr)
        return 3

    meta = {}
    mpath = base / "meta.json"
    if mpath.exists():
        meta = json.loads(mpath.read_text())
    obj = meta.get("object", POPULAR_OBJECT)
    pops = meta.get("pops", 3)

    fanin = origin_object_delta(base, obj)
    origin_total = origin_requests_delta(base)

    lines = [f"# Origin fan-in - {base.name}", ""]
    lines.append(f"- popular object: `{obj}`")
    lines.append(f"- PoP count: {pops}")
    lines.append(f"- **origin fetches for the object on expiry (fan-in): {int(fanin)}**")
    lines.append(f"- total origin requests during the burst: {int(origin_total)}")
    if fanin <= 1.5:
        lines.append("- Fan-in collapsed to ~one: the origin shield absorbed the herd.")
    elif fanin >= pops - 0.5:
        lines.append(f"- Fan-in ~ PoP count ({pops}): every PoP fetched the origin independently (shielding off).")
    report = "\n".join(lines) + "\n"
    (base / "fanin.md").write_text(report)
    (base / "fanin.json").write_text(json.dumps(
        {"label": base.name, "object": obj, "pops": pops, "fanin": fanin, "origin_total": origin_total},
        indent=2,
    ))
    print(report)
    print(f"Wrote {base/'fanin.md'} and {base/'fanin.json'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
