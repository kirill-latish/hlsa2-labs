#!/usr/bin/env python3
"""analyze-staleness.py - report fraction_stale and max_staleness_seconds
for each invalidation strategy from the writer/reader race.

Reads perf/results/staleness/<sub>/summary.json (the loadgen snapshot,
which carries fresh_samples, stale_samples, fraction_stale, and
max_staleness_seconds).

Writes perf/results/staleness/report.md and prints it.

Usage:
    python3 scripts/analyze-staleness.py perf/results/staleness
"""

from __future__ import annotations

import json
import sys
from pathlib import Path


def load_summary(d: Path) -> dict | None:
    p = d / "summary.json"
    if not p.exists():
        return None
    return json.loads(p.read_text())


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    if not base.exists():
        print(f"ERROR: {base} not found - run make bench-staleness first", file=sys.stderr)
        return 3

    lines = ["# Invalidation staleness analysis\n"]
    lines.append("| strategy | samples | stale | fraction_stale | max_staleness_s |")
    lines.append("|----------|---------|-------|----------------|-----------------|")
    found = False
    for sub, label in [("ttl-only", "ttl-only"), ("explicit", "explicit-invalidate")]:
        s = load_summary(base / sub)
        if s is None:
            lines.append(f"| {label} | (no run) | - | - | - |")
            continue
        found = True
        fresh = int(s.get("fresh_samples", 0))
        stale = int(s.get("stale_samples", 0))
        samples = fresh + stale
        frac = s.get("fraction_stale", 0.0)
        maxs = s.get("max_staleness_seconds", 0.0)
        lines.append(f"| {label} | {samples} | {stale} | {frac*100:.3f}% | {maxs:.2f} |")

    if not found:
        print(f"ERROR: no summary.json under {base}/ttl-only or {base}/explicit", file=sys.stderr)
        return 3

    lines.append("")
    lines.append("Explicit invalidation should drive fraction_stale toward zero "
                 "(assuming every writer path invalidates); TTL-only shows a "
                 "predictable rate bounded by the TTL window.")

    out = base / "report.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
