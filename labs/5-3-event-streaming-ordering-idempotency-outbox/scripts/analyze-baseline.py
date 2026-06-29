#!/usr/bin/env python3
"""analyze-baseline.py - aggregate the baseline runs into median + sigma
for throughput, consumer lag (count + time), and the duplicate rate.

Reads each runN/summary.json written by run-bench-baseline.sh, writes a
Markdown report at <baseline-dir>/report.md, and prints it.

Usage:
    python3 scripts/analyze-baseline.py perf/results/baseline
"""
from __future__ import annotations

import json
import sys
from pathlib import Path
from statistics import median, pstdev


def run_dirs(base: Path) -> list[Path]:
    return sorted(p for p in base.glob("run*") if (p / "summary.json").exists())


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    runs = run_dirs(base)
    if not runs:
        print(f"ERROR: no run*/summary.json under {base}", file=sys.stderr)
        return 3

    rows = [json.loads((r / "summary.json").read_text()) for r in runs]
    keys = [
        ("throughput_eps", "throughput (events/s)"),
        ("lag_count_peak", "consumer lag count (peak)"),
        ("lag_time_peak_s", "consumer lag time (peak, s)"),
        ("duplicate_rate", "duplicate rate"),
    ]

    lines: list[str] = [f"# Baseline analysis - {base.name}\n", f"_{len(rows)} runs_\n"]
    lines.append("\n| run | throughput eps | lag count | lag time s | duplicate rate |")
    lines.append("|-----|---------------:|----------:|-----------:|---------------:|")
    for r in rows:
        lines.append(
            f"| {r['run']} | {r['throughput_eps']:.2f} | {r['lag_count_peak']:.0f} | "
            f"{r['lag_time_peak_s']:.2f} | {r['duplicate_rate']:.5f} |"
        )

    lines.append("\n## Aggregated (median, sigma)\n")
    for k, label in keys:
        vals = [float(r[k]) for r in rows]
        med = median(vals)
        sig = pstdev(vals) if len(vals) > 1 else 0.0
        lines.append(f"- **{label}**: median={med:.5f} sigma={sig:.5f}")

    dup = median([float(r["duplicate_rate"]) for r in rows])
    lines.append("")
    if dup == 0:
        lines.append("> NOTE: duplicate rate is a flat zero - dedup was never exercised "
                     "or events were lost. A healthy at-least-once pipeline is low-but-nonzero.")
    else:
        lines.append("> Duplicate rate is low-but-nonzero: normal at-least-once behavior.")

    out_md = base / "report.md"
    out_md.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out_md}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
