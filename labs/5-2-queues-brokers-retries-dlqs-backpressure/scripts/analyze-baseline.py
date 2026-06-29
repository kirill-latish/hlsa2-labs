#!/usr/bin/env python3
"""analyze-baseline.py - aggregate the baseline pipeline runs, reporting
lag (count AND time), throughput, processing p50/p99, and DLQ rate
together with run-to-run sigma. Throughput alone is misleading; this
reports the metric pair throughput-only views hide.

Usage:
    python3 scripts/analyze-baseline.py perf/results/baseline
"""

from __future__ import annotations

import math
import sys
from pathlib import Path
from statistics import median, pstdev

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from analyze_lib import run_dirs, summarize_run  # noqa: E402


def agg(rows, key):
    vals = [r[key] for r in rows if not (isinstance(r[key], float) and math.isnan(r[key]))]
    if not vals:
        return (math.nan, math.nan)
    return (median(vals), pstdev(vals) if len(vals) > 1 else 0.0)


def fmt(v, unit=""):
    if v is None or (isinstance(v, float) and math.isnan(v)):
        return "n/a"
    return f"{v:.3f}{unit}"


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    runs = run_dirs(base)
    if not runs:
        print(f"ERROR: no run* subdirs under {base}", file=sys.stderr)
        return 3
    rows = [summarize_run(r) for r in runs]

    lines = [f"# Baseline analysis - {base.name}\n", f"_{len(rows)} runs_\n"]
    lines.append("\n| run | throughput/s | lag_count | lag_age_s | p50 ms | p99 ms | retries/s | dlq/s |")
    lines.append("|-----|-------------:|----------:|----------:|-------:|-------:|----------:|------:|")
    for r in rows:
        lines.append(
            f"| {r['run']} | {fmt(r['throughput_rps'])} | {fmt(r['lag_count'])} | {fmt(r['lag_age_s'])} | "
            f"{fmt(r['p50'])} | {fmt(r['p99'])} | {fmt(r['retry_rps'])} | {fmt(r['dlq_rps'])} |"
        )
    lines.append("\n## Aggregated (median, sigma)\n")
    for key, label, unit in [
        ("throughput_rps", "throughput", " msg/s"),
        ("lag_count", "lag (count)", ""),
        ("lag_age_s", "lag (time)", " s"),
        ("p50", "processing p50", " ms"),
        ("p99", "processing p99", " ms"),
        ("dlq_rps", "dlq rate", " msg/s"),
    ]:
        med, sig = agg(rows, key)
        lines.append(f"- **{label}** median={fmt(med, unit)} sigma={fmt(sig, unit)}")

    out = base / "report.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
