#!/usr/bin/env python3
"""analyze-backpressure.py - under sustained overload, decide whether
lag stabilized (backpressure honored) or grew unbounded (ignored).
Producer error rate is the tell: non-zero means the broker's
reject-publish reached the producer.

Usage:
    python3 scripts/analyze-backpressure.py perf/results/backpressure
"""

from __future__ import annotations

import json
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


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    runs = run_dirs(base)
    if not runs:
        print(f"ERROR: no run* subdirs under {base}", file=sys.stderr)
        return 3
    rd = runs[0]
    r = summarize_run(rd)
    meta = json.loads((rd / "meta.json").read_text()) if (rd / "meta.json").exists() else {}

    lines = [f"# Backpressure analysis - {base.name}\n"]
    lines.append(f"\nSustained overload: rate={meta.get('rate_rps','?')} msg/s, backpressure_honored={meta.get('backpressure','?')}\n")
    lines.append("\n| metric | value |")
    lines.append("|--------|------:|")
    lines.append(f"| throughput (msg/s) | {fmt(r['throughput_rps'])} |")
    lines.append(f"| lag (count, final) | {fmt(r['lag_count'])} |")
    lines.append(f"| lag (time, s) | {fmt(r['lag_age_s'])} |")
    lines.append(f"| produced | {fmt(r['produced'])} |")
    lines.append(f"| producer errors | {fmt(r['produce_errors'])} |")
    lines.append(f"| processing p99 (ms) | {fmt(r['p99'])} |")

    lines.append("\n## Decision\n")
    lag = r["lag_count"]
    errs = r["produce_errors"]
    if errs > 0 and (lag != lag or lag < 20000):  # NaN-safe
        verdict = ("BACKPRESSURE HONORED: producer saw rejections and slowed; "
                   "lag is bounded. Graceful degradation.")
    elif lag == lag and lag >= 20000:
        verdict = ("BACKPRESSURE IGNORED: lag grew toward the queue bound with "
                   "few/no producer errors. The producer must be fixed to honor "
                   "broker backpressure.")
    else:
        verdict = ("INCONCLUSIVE: inspect the lag-over-time pane in Grafana "
                   "(docs/img/backpressure-lag.png) to classify the regime.")
    lines.append(f"- {verdict}")

    out = base / "report.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
