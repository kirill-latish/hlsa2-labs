#!/usr/bin/env python3
"""analyze-poison.py - characterise a poison-injection run: retry rate
on the wedged message, cluster throughput (should drop under unbounded
retries; recover under bounded-retry+DLQ), lag growth, and DLQ count.

Usage:
    python3 scripts/analyze-poison.py perf/results/poison-baseline
"""

from __future__ import annotations

import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from analyze_lib import run_dirs, summarize_run  # noqa: E402


def fmt(v, unit=""):
    if v is None:
        return "n/a"
    try:
        return f"{float(v):.3f}{unit}"
    except (TypeError, ValueError):
        return str(v)


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    runs = run_dirs(base)
    if not runs:
        print(f"ERROR: no run* subdirs under {base}", file=sys.stderr)
        return 3
    r = summarize_run(runs[0])

    lines = [f"# Poison-message analysis - {base.name}\n"]
    lines.append("\n| metric | value |")
    lines.append("|--------|------:|")
    lines.append(f"| cluster throughput (msg/s) | {fmt(r['throughput_rps'])} |")
    lines.append(f"| retry rate (retries/s) | {fmt(r['retry_rps'])} |")
    lines.append(f"| total retries | {fmt(r['retries'])} |")
    lines.append(f"| DLQ count | {fmt(r['dlq'])} |")
    lines.append(f"| lag (count, final) | {fmt(r['lag_count'])} |")
    lines.append(f"| lag (time, s) | {fmt(r['lag_age_s'])} |")
    lines.append(f"| processing p99 (ms) | {fmt(r['p99'])} |")

    lines.append("\n## Interpretation\n")
    if r["dlq"] >= 1:
        lines.append(
            f"- Poison reached the DLQ ({int(r['dlq'])} message(s)); bounded retries "
            f"broke the loop and the consumer slot was freed."
        )
    elif r["retry_rps"] > 50 and r["throughput_rps"] < 50:
        lines.append(
            "- High retry rate with collapsed throughput: the poison message is "
            "wedging the fleet under unbounded retries (no DLQ escape)."
        )
    else:
        lines.append("- Inspect the dashboard - the signature is between the two regimes.")

    out = base / "report.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
