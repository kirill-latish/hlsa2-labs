#!/usr/bin/env python3
"""compare-idempotency.py BEFORE AFTER - side-by-side of two
verify-exactly-once result.json files (typically idempotency-naive vs
idempotency-after). Writes a Markdown report under the AFTER dir.

Usage:
    python3 scripts/compare-idempotency.py idempotency-naive idempotency-after
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent / "perf" / "results"


def load(label: str) -> dict:
    p = ROOT / label / "result.json"
    if not p.exists():
        print(f"ERROR: {p} not found - run verify-exactly-once LABEL={label} first", file=sys.stderr)
        sys.exit(3)
    return json.loads(p.read_text())


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    before_l, after_l = sys.argv[1], sys.argv[2]
    b, a = load(before_l), load(after_l)

    lines = [
        f"# Idempotency comparison: {before_l} vs {after_l}\n",
        "| metric | before | after |",
        "|--------|-------:|------:|",
        f"| side_effects_total | {b['side_effects_total']} | {a['side_effects_total']} |",
        f"| unique_events | {b['unique_events']} | {a['unique_events']} |",
        f"| side_effect_ratio | {b['side_effect_ratio']} | {a['side_effect_ratio']} |",
        f"| exactly_once_effect | {b['exactly_once_effect']} | {a['exactly_once_effect']} |",
        f"| duplicates_suppressed_total | {b['duplicates_suppressed_total']} | {a['duplicates_suppressed_total']} |",
        "",
    ]
    if b["side_effect_ratio"] > 1.0 and a["exactly_once_effect"]:
        lines.append("**PASS**: naive over-applied (ratio > 1); idempotent achieved exactly-once effect "
                     "(side_effects == unique_events) despite identical duplicate injection + crash.")
    else:
        lines.append("**REVIEW**: expected before ratio > 1 and after exactly-once. Re-check the run order.")

    report = "\n".join(lines) + "\n"
    out = ROOT / after_l / "compare-vs-before.md"
    out.write_text(report)
    print(report)
    print(f"Wrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
