#!/usr/bin/env python3
"""analyze-baseline.py - aggregate the 3-run baseline and, critically,
report the EDGE OVERHEAD separately from total latency.

Reads each runN/edge-metrics.txt (edge Prometheus snapshot) and
runN/summary.json (loadgen). Writes a Markdown report at
<baseline-dir>/report.md and prints it.

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
from analyze_baseline_lib import run_dirs, summarize_run  # noqa: E402


def aggregate(rows: list[dict]) -> dict[str, tuple[float, float]]:
    out = {}
    for k in ("overhead_p50", "overhead_p99", "total_p50", "total_p99",
              "throughput_rps", "success_ratio"):
        values = [r[k] for r in rows if not (isinstance(r[k], float) and math.isnan(r[k]))]
        if not values:
            out[k] = (math.nan, math.nan)
            continue
        out[k] = (median(values), pstdev(values) if len(values) > 1 else 0.0)
    return out


def fmt(v: float, unit: str = "") -> str:
    if v is None or (isinstance(v, float) and math.isnan(v)):
        return "   n/a"
    return f"{v:8.3f}{unit}"


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
    agg = aggregate(rows)

    lines: list[str] = []
    lines.append(f"# Baseline analysis - {base.name}\n")
    lines.append(f"_{len(rows)} runs. Edge overhead is reported SEPARATELY from backend latency._\n")
    lines.append("\n| run | offered | served | throughput rps | edge p50 ms | edge p99 ms | total p50 ms | total p99 ms | success |")
    lines.append("|-----|---------|--------|----------------|-------------|-------------|--------------|--------------|---------|")
    for r in rows:
        lines.append(
            f"| {r['run']} | {r['offered']} | {r['served']} | {fmt(r['throughput_rps'])} | "
            f"{fmt(r['overhead_p50'])} | {fmt(r['overhead_p99'])} | "
            f"{fmt(r['total_p50'])} | {fmt(r['total_p99'])} | "
            f"{r['success_ratio']*100:.2f}% |"
        )
    lines.append("\n## Aggregated (median, sigma)\n")
    for k, label, unit in [
        ("overhead_p50", "EDGE overhead p50", " ms"),
        ("overhead_p99", "EDGE overhead p99", " ms"),
        ("total_p50", "total p50", " ms"),
        ("total_p99", "total p99", " ms"),
        ("throughput_rps", "throughput", " rps"),
        ("success_ratio", "success ratio", ""),
    ]:
        med, sig = agg[k]
        lines.append(f"- **{label}** median={fmt(med, unit)} sigma={fmt(sig, unit)}")

    o50 = agg["overhead_p50"][0]
    t50 = agg["total_p50"][0]
    if not (math.isnan(o50) or math.isnan(t50)) and t50 > 0:
        lines.append(
            f"\n> The edge tier adds **{o50:.3f} ms (p50)** of pure overhead - "
            f"{o50/t50*100:.1f}% of the {t50:.3f} ms total. This is the number "
            f"teams never isolate."
        )

    out_md = base / "report.md"
    out_md.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print()
    print(f"Wrote {out_md}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
