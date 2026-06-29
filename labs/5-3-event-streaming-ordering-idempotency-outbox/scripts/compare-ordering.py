#!/usr/bin/env python3
"""compare-ordering.py BEFORE AFTER - side-by-side of two bench-ordering
summary.json files (ordering-wrong vs ordering-entity). Writes a
Markdown report under the AFTER dir.

Usage:
    python3 scripts/compare-ordering.py ordering-wrong ordering-entity
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent / "perf" / "results"


def load(label: str) -> dict:
    p = ROOT / label / "summary.json"
    if not p.exists():
        print(f"ERROR: {p} not found - run bench-ordering for {label} first", file=sys.stderr)
        sys.exit(3)
    return json.loads(p.read_text())


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    before_l, after_l = sys.argv[1], sys.argv[2]
    b, a = load(before_l), load(after_l)

    lines = [
        f"# Ordering comparison: {before_l} vs {after_l}\n",
        "| metric | before | after |",
        "|--------|-------:|------:|",
        f"| key_strategy | {b['key_strategy']} | {a['key_strategy']} |",
        f"| ordering_violations | {b['ordering_violations']} | {a['ordering_violations']} |",
        f"| events_consumed | {b['events_consumed']} | {a['events_consumed']} |",
        f"| ordering_violation_rate | {b['ordering_violation_rate']} | {a['ordering_violation_rate']} |",
        "",
    ]
    if b["ordering_violations"] > 0 and a["ordering_violations"] == 0:
        lines.append("**PASS**: the wrong key produced ordering violations; the per-entity key "
                     "drove them to zero (all of an order's events share a partition).")
    elif b["ordering_violation_rate"] > a["ordering_violation_rate"]:
        lines.append("**IMPROVED**: per-entity key reduced the ordering-violation rate "
                     f"from {b['ordering_violation_rate']} to {a['ordering_violation_rate']}.")
    else:
        lines.append("**REVIEW**: expected the wrong key to show more violations than the entity key.")

    report = "\n".join(lines) + "\n"
    out = ROOT / after_l / "compare-vs-before.md"
    out.write_text(report)
    print(report)
    print(f"Wrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
