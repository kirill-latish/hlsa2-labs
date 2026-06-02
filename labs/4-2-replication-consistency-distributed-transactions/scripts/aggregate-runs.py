#!/usr/bin/env python3
"""Aggregate run1..runN/summary.json under a label dir into a single
report.md + summary.json that the comparison scripts read.
"""
from __future__ import annotations

import json
import statistics
import sys
from pathlib import Path
from typing import List


def load_runs(base: Path) -> List[dict]:
    out = []
    for d in sorted(base.iterdir()):
        if d.is_dir() and d.name.startswith("run"):
            p = d / "summary.json"
            if p.exists():
                out.append(json.loads(p.read_text()))
    return out


def main(argv: List[str]) -> int:
    if len(argv) < 2:
        print(f"usage: {argv[0]} <perf/results/2pc|saga/LABEL>", file=sys.stderr)
        return 2
    base = Path(argv[1])
    if not base.is_dir():
        print(f"missing: {base}", file=sys.stderr)
        return 2

    runs = load_runs(base)
    if not runs:
        print(f"no run*/summary.json under {base}", file=sys.stderr)
        return 2

    success = [r.get("success_rate", 0) for r in runs]
    p99 = [r.get("latency_ms", {}).get("p99", 0) for r in runs]
    p999 = [r.get("latency_ms", {}).get("p999", 0) for r in runs]
    requests = sum(r.get("requests", 0) for r in runs)
    failed = sum(r.get("failed", 0) for r in runs)
    compensated = sum(r.get("compensated_count", 0) for r in runs)

    summary = {
        "label": base.name,
        "mode": runs[0].get("mode") if runs else "?",
        "runs": len(runs),
        "success_rate": {
            "median": statistics.median(success),
            "min": min(success),
            "max": max(success),
            "sigma": statistics.pstdev(success) if len(success) > 1 else 0.0,
        },
        "latency_p99_ms": {
            "median": statistics.median(p99),
            "min": min(p99),
            "max": max(p99),
            "sigma": statistics.pstdev(p99) if len(p99) > 1 else 0.0,
        },
        "latency_p999_ms": {
            "median": statistics.median(p999),
        },
        "requests_total": requests,
        "failed_total": failed,
        "compensated_total": compensated,
    }
    out = base / "summary.json"
    with out.open("w") as f:
        json.dump(summary, f, indent=2)
    md = base / "report.md"
    with md.open("w") as f:
        f.write(f"# {summary['label']} ({summary['mode']})\n\n")
        f.write(f"- Runs: {summary['runs']}\n")
        f.write(f"- Total requests: {requests}, failed: {failed}, compensated: {compensated}\n")
        f.write(f"- Success rate: median={summary['success_rate']['median']:.4f} sigma={summary['success_rate']['sigma']:.4f}\n")
        f.write(f"- p99 latency (ms): median={summary['latency_p99_ms']['median']:.2f} sigma={summary['latency_p99_ms']['sigma']:.2f}\n")
        f.write(f"- p99.9 latency (ms): median={summary['latency_p999_ms']['median']:.2f}\n")
    print(f"wrote {out} + {md}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
