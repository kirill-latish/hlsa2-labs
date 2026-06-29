#!/usr/bin/env python3
"""compare-distribution.py - per-backend request distribution and the
max-vs-mean skew ratio, for one run or a BEFORE/AFTER pair.

The per-run request count is the delta of the edge's /admin/status
`requests` counter between status-start.json and status-end.json, so it
isolates the traffic of that single labelled run.

Usage:
    python3 scripts/compare-distribution.py perf/results/distribution/dist-rr
    python3 scripts/compare-distribution.py perf/results/distribution/dist-rr perf/results/distribution/dist-lc
"""

from __future__ import annotations

import json
import math
import sys
from pathlib import Path


def deltas(run_dir: Path) -> dict[str, int]:
    start = json.loads((run_dir / "status-start.json").read_text())
    end = json.loads((run_dir / "status-end.json").read_text())
    s = {b["id"]: b["requests"] for b in start.get("backends", [])}
    e = {b["id"]: b["requests"] for b in end.get("backends", [])}
    return {k: int(e.get(k, 0) - s.get(k, 0)) for k in sorted(e)}


def max_vs_mean(counts: dict[str, int]) -> float:
    vals = [v for v in counts.values()]
    if not vals:
        return math.nan
    mean = sum(vals) / len(vals)
    return max(vals) / mean if mean > 0 else math.nan


def algo_of(run_dir: Path) -> str:
    meta = run_dir / "meta.json"
    if meta.exists():
        return json.loads(meta.read_text()).get("algo", "?")
    return "?"


def render_one(run_dir: Path) -> list[str]:
    d = deltas(run_dir)
    total = sum(d.values())
    lines = [f"### {run_dir.name} (algo={algo_of(run_dir)})\n",
             "\n| backend | requests | share |",
             "|---------|---------:|------:|"]
    for k, v in d.items():
        share = (v / total * 100) if total else 0.0
        lines.append(f"| {k} | {v} | {share:.1f}% |")
    lines.append(f"\n- total routed: {total}")
    lines.append(f"- **max-vs-mean skew: {max_vs_mean(d):.3f}**")
    return lines


def main() -> int:
    if len(sys.argv) not in (2, 3):
        print(__doc__, file=sys.stderr)
        return 2
    dirs = [Path(a) for a in sys.argv[1:]]
    for d in dirs:
        if not d.exists():
            print(f"ERROR: no such dir {d}", file=sys.stderr)
            return 3

    lines = ["# Load distribution\n"]
    for d in dirs:
        lines += render_one(d)
        lines.append("")

    if len(dirs) == 2:
        a, b = dirs
        ra, rb = max_vs_mean(deltas(a)), max_vs_mean(deltas(b))
        lines.append("## Verdict\n")
        lines.append(f"- {a.name} ({algo_of(a)}) skew = {ra:.3f}")
        lines.append(f"- {b.name} ({algo_of(b)}) skew = {rb:.3f}")
        lines.append(
            "\nRound-robin spreads requests by COUNT (near-uniform) but ignores "
            "per-backend capacity, so the slow backend backs up (see the in-flight "
            "panel). Least-conn skews the COUNT toward faster backends precisely "
            "because it routes on live load - that is the rebalancing the topic "
            "predicts for uneven request costs."
        )
        out = b.parent / "compare-distribution.md"
    else:
        out = dirs[0] / "report.md"

    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
