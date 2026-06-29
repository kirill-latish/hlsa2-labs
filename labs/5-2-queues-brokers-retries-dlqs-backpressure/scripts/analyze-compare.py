#!/usr/bin/env python3
"""analyze-compare.py - side-by-side BEFORE vs AFTER for the poison
experiment. Reports the retry-rate, throughput, lag, and DLQ before and
after the fix, and writes a compare-vs-before.md under the AFTER dir.

Usage:
    python3 scripts/analyze-compare.py perf/results/poison-baseline perf/results/poison-after
"""

from __future__ import annotations

import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from analyze_lib import run_dirs, summarize_run  # noqa: E402


def fmt(v, unit=""):
    try:
        return f"{float(v):.3f}{unit}"
    except (TypeError, ValueError):
        return "n/a"


def first(base: Path):
    runs = run_dirs(base)
    return summarize_run(runs[0]) if runs else None


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    before_dir, after_dir = Path(sys.argv[1]), Path(sys.argv[2])
    b = first(before_dir)
    a = first(after_dir)
    if not b or not a:
        print(f"ERROR: missing runs before={bool(b)} after={bool(a)}", file=sys.stderr)
        return 3

    keys = [
        ("throughput_rps", "throughput (msg/s)"),
        ("retry_rps", "retry rate (retries/s)"),
        ("dlq", "DLQ (total)"),
        ("lag_count", "lag (count)"),
        ("lag_age_s", "lag (time, s)"),
        ("p99", "processing p99 (ms)"),
    ]

    print(f"\nCompare BEFORE={before_dir.name}  AFTER={after_dir.name}\n")
    print(f"  {'metric':<26} {'before':>12} {'after':>12} {'delta':>12}")
    lines = [
        f"# Compare: BEFORE={before_dir.name} vs AFTER={after_dir.name}\n",
        "\n| metric | before | after | delta |",
        "|--------|-------:|------:|------:|",
    ]
    for key, label in keys:
        bv, av = b[key], a[key]
        try:
            delta = float(av) - float(bv)
            ds = fmt(delta)
        except (TypeError, ValueError):
            ds = "n/a"
        print(f"  {label:<26} {fmt(bv):>12} {fmt(av):>12} {ds:>12}")
        lines.append(f"| {label} | {fmt(bv)} | {fmt(av)} | {ds} |")

    # Headline: did the fix break the poison loop?
    verdict = (
        "FIX EFFECTIVE: poison dead-lettered and throughput recovered."
        if a["dlq"] >= 1 and a["throughput_rps"] >= b["throughput_rps"]
        else "INSPECT: the after-run did not clearly dominate the before-run."
    )
    lines.append(f"\n**Decision**: {verdict}\n")
    print(f"\n  decision = {verdict}")

    out = after_dir / "compare-vs-before.md"
    out.write_text("\n".join(lines) + "\n")
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
