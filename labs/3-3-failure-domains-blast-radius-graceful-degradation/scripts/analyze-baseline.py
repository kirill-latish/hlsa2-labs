#!/usr/bin/env python3
"""analyze-baseline.py - aggregate per-run success rate + latency percentiles
across the 3-run baseline.

Reads each runN/summary.json (the loadgen /summary snapshot) and the
corresponding runN/gateway-metrics.txt (Prometheus textfile). Latency
percentiles come from the gateway's
lab33_http_request_duration_seconds_bucket histogram for endpoint
/checkout, restricted to 2xx responses so the percentile is honest.

Writes a Markdown report at <baseline-dir>/report.md and prints it.

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
    for k in ("p50", "p95", "p99", "p999", "gateway_success_ratio", "loadgen_success_ratio"):
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
    lines.append(f"_{len(rows)} runs_\n")
    lines.append("\n| run | offered | served | success_full | degraded | failed | shed | p50 ms | p95 ms | p99 ms | p99.9 ms | success |")
    lines.append("|-----|---------|--------|--------------|----------|--------|------|--------|--------|--------|----------|---------|")
    for r in rows:
        lines.append(
            f"| {r['run']} | {r['offered']} | {r['served']} | {r['gateway_success_full']} | "
            f"{r['gateway_success_degraded']} | {r['gateway_failed']} | {r['gateway_shed']} | "
            f"{fmt(r['p50'])} | {fmt(r['p95'])} | {fmt(r['p99'])} | {fmt(r['p999'])} | "
            f"{r['gateway_success_ratio']*100:.2f}% |"
        )
    lines.append("\n## Aggregated (median, sigma)\n")
    for k, label, unit in [
        ("p50", "p50", " ms"),
        ("p95", "p95", " ms"),
        ("p99", "p99", " ms"),
        ("p999", "p99.9", " ms"),
        ("gateway_success_ratio", "success_ratio (gateway)", ""),
        ("loadgen_success_ratio", "success_ratio (loadgen)", ""),
    ]:
        med, sig = agg[k]
        lines.append(f"- **{label}** median={fmt(med, unit)} sigma={fmt(sig, unit)}")

    out_md = base / "report.md"
    out_md.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print()
    print(f"Wrote {out_md}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
