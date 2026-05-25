#!/usr/bin/env python3
"""analyze-compare.py - side-by-side BEFORE vs AFTER decision using the
2-sigma rule from lab 3-2's analyze-regression.py.

Reads runN/ dirs under each side, recomputes critical-journey success
ratio + blast radius + p99 latency, then applies:

    |delta_metric| > 2 * max(sigma_before, sigma_after)

to call IMPROVED / REGRESSED / noise. The "metric" used for the
decision is the critical-journey success ratio (higher is better);
p99 is reported but not used to gate the decision.

Usage:
    python3 scripts/analyze-compare.py perf/results/faulted-before perf/results/faulted-after
"""

from __future__ import annotations

import math
import sys
from pathlib import Path
from statistics import median, pstdev

# Re-use the metric parsers from analyze-baseline.py by importing.
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from analyze_baseline_lib import summarize_run, run_dirs  # type: ignore  # noqa: E402


def aggregate(rows: list[dict], keys: list[str]) -> dict[str, tuple[float, float]]:
    out = {}
    for k in keys:
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
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    before_dir = Path(sys.argv[1])
    after_dir = Path(sys.argv[2])
    before_runs = run_dirs(before_dir)
    after_runs = run_dirs(after_dir)
    if not before_runs or not after_runs:
        print(f"ERROR: missing runs - before={len(before_runs)} after={len(after_runs)}", file=sys.stderr)
        return 3
    if len(before_runs) < 3 or len(after_runs) < 3:
        print(f"WARN: rubric expects 3 runs per side; got before={len(before_runs)} after={len(after_runs)}", file=sys.stderr)

    b_rows = [summarize_run(r) for r in before_runs]
    a_rows = [summarize_run(r) for r in after_runs]

    metric_keys = ["gateway_success_ratio", "p99", "p95", "p50"]
    b_agg = aggregate(b_rows, metric_keys)
    a_agg = aggregate(a_rows, metric_keys)

    print()
    print(f"Compare BEFORE={before_dir.name} ({len(b_rows)} runs)  AFTER={after_dir.name} ({len(a_rows)} runs)")
    print()
    print(f"  {'metric':<22} {'before_med':>11} {'before_s':>10}   {'after_med':>11} {'after_s':>10}   {'delta':>10}")
    for k in metric_keys:
        bm, bs = b_agg[k]
        am, as_ = a_agg[k]
        delta = am - bm if not (math.isnan(am) or math.isnan(bm)) else math.nan
        unit = "" if k == "gateway_success_ratio" else " ms"
        print(f"  {k:<22} {fmt(bm, unit)} {fmt(bs, unit)}   {fmt(am, unit)} {fmt(as_, unit)}   {fmt(delta, unit)}")

    # Decision is on success_ratio (higher is better).
    bm, bs = b_agg["gateway_success_ratio"]
    am, as_ = a_agg["gateway_success_ratio"]
    delta = am - bm
    threshold = 2.0 * max(bs, as_)
    print()
    print(f"  delta_success_ratio = {fmt(delta)}")
    print(f"  2*max(sigma)        = {fmt(threshold)}")
    if math.isnan(delta) or math.isnan(threshold):
        verdict = "INSUFFICIENT-DATA"
    elif abs(delta) <= threshold:
        verdict = "noise (within 2 sigma)"
    elif delta > 0:
        verdict = "IMPROVED (>2 sigma)"
    else:
        verdict = "REGRESSED (>2 sigma)"
    print(f"  decision            = {verdict}")

    # Write a tiny report.
    out = after_dir / "compare-vs-before.md"
    lines = [
        f"# Compare: BEFORE={before_dir.name} vs AFTER={after_dir.name}\n",
        f"\n_{len(b_rows)} vs {len(a_rows)} runs_\n",
        "\n| metric | before_median | before_sigma | after_median | after_sigma | delta |",
        "|--------|--------------:|-------------:|-------------:|------------:|------:|",
    ]
    for k in metric_keys:
        bm, bs = b_agg[k]
        am, as_ = a_agg[k]
        delta_v = am - bm if not (math.isnan(am) or math.isnan(bm)) else math.nan
        unit = "" if k == "gateway_success_ratio" else " ms"
        lines.append(f"| {k} | {fmt(bm, unit)} | {fmt(bs, unit)} | {fmt(am, unit)} | {fmt(as_, unit)} | {fmt(delta_v, unit)} |")
    lines.append(f"\n**Decision** (on `gateway_success_ratio`, 2-sigma rule): {verdict}\n")
    out.write_text("\n".join(lines) + "\n")
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
