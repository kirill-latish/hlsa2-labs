#!/usr/bin/env python3
"""compare-stampede.py - print the fan-in ratio before/after the fix and
the factor of improvement. Reads each label's fan_in_ratio.json (written
by analyze-stampede.py).

Usage:
    python3 scripts/compare-stampede.py perf/results/stampede-baseline perf/results/stampede-after
"""

from __future__ import annotations

import json
import sys
from pathlib import Path


def load_ratio(d: Path) -> dict:
    p = d / "fan_in_ratio.json"
    if not p.exists():
        print(f"ERROR: {p} not found - run make analyze-stampede LABEL={d.name} first", file=sys.stderr)
        raise SystemExit(3)
    return json.loads(p.read_text())


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    before = load_ratio(Path(sys.argv[1]))
    after = load_ratio(Path(sys.argv[2]))

    b = before["fan_in_ratio"]
    a = after["fan_in_ratio"]
    factor = (b / a) if a > 0 else float("inf")

    lines = [
        "# Stampede comparison (fan-in at expiration)\n",
        "| label | coalescing | jitter% | source_fetches | fan_in_ratio | read p99 ms |",
        "|-------|-----------|---------|----------------|--------------|-------------|",
        f"| {before['label']} | {before['coalescing']} | {before['ttl_jitter_pct']} | "
        f"{before['source_fetches']:.0f} | {b:.1f} | {before['read_p99_ms']:.1f} |",
        f"| {after['label']} | {after['coalescing']} | {after['ttl_jitter_pct']} | "
        f"{after['source_fetches']:.0f} | {a:.1f} | {after['read_p99_ms']:.1f} |",
        "",
        f"**Fan-in reduced {factor:.1f}x** ({b:.1f} -> {a:.1f}).",
    ]
    out = Path(sys.argv[2]) / "compare-vs-before.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
