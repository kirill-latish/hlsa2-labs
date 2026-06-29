#!/usr/bin/env python3
"""compare-consistency.py BEFORE AFTER - side-by-side of two
analyze-consistency result.json files (dualwrite-naive vs
dualwrite-outbox). Writes a Markdown report under the AFTER dir.

Usage:
    python3 scripts/compare-consistency.py dualwrite-naive dualwrite-outbox
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent / "perf" / "results"


def load(label: str) -> dict:
    p = ROOT / label / "result.json"
    if not p.exists():
        print(f"ERROR: {p} not found - run analyze-consistency LABEL={label} first", file=sys.stderr)
        sys.exit(3)
    return json.loads(p.read_text())


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    before_l, after_l = sys.argv[1], sys.argv[2]
    b, a = load(before_l), load(after_l)

    lines = [
        f"# Dual-write consistency comparison: {before_l} vs {after_l}\n",
        "| metric | before | after |",
        "|--------|-------:|------:|",
        f"| orders_written | {b['orders_written']} | {a['orders_written']} |",
        f"| projection_orders | {b['projection_orders']} | {a['projection_orders']} |",
        f"| orphaned_state_changes | {b['orphaned_state_changes']} | {a['orphaned_state_changes']} |",
        f"| consistent | {b['consistent']} | {a['consistent']} |",
        "",
    ]
    if b["orphaned_state_changes"] > 0 and a["orphaned_state_changes"] == 0:
        lines.append("**PASS**: the naive dual-write left orphaned state after the crash "
                     "(DB changed, no event emitted); the outbox produced zero orphans under "
                     "the identical crash because both writes commit in one local transaction.")
    else:
        lines.append("**REVIEW**: expected before orphans > 0 and after orphans == 0.")

    report = "\n".join(lines) + "\n"
    out = ROOT / after_l / "compare-vs-before.md"
    out.write_text(report)
    print(report)
    print(f"Wrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
