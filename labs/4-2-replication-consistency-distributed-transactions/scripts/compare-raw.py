#!/usr/bin/env python3
"""Compare two read-after-write modes side-by-side.

Reads $BEFORE and $AFTER (mode names like naive, session-pin) from
perf/results/raw/<mode>/summary.json and prints a 2-column table.
"""
from __future__ import annotations

import json
import os
import sys
from pathlib import Path


def load(mode: str) -> dict:
    p = Path("perf/results/raw") / mode / "summary.json"
    if not p.exists():
        print(f"missing: {p}", file=sys.stderr)
        sys.exit(2)
    return json.loads(p.read_text())


def main() -> int:
    before = os.environ.get("BEFORE", "naive")
    after = os.environ.get("AFTER", "session-pin")
    if before == after:
        print("BEFORE and AFTER are identical; nothing to compare", file=sys.stderr)
        return 2
    a = load(before)
    b = load(after)

    rows = [
        ("Reads", a.get("reads"), b.get("reads")),
        ("Violations", a.get("violations"), b.get("violations")),
        ("Violation rate", f"{a.get('violation_rate', 0):.4%}", f"{b.get('violation_rate', 0):.4%}"),
        ("Read p50 (ms)", round(a.get("latency_ms", {}).get("p50", 0), 3), round(b.get("latency_ms", {}).get("p50", 0), 3)),
        ("Read p95 (ms)", round(a.get("latency_ms", {}).get("p95", 0), 3), round(b.get("latency_ms", {}).get("p95", 0), 3)),
        ("Read p99 (ms)", round(a.get("latency_ms", {}).get("p99", 0), 3), round(b.get("latency_ms", {}).get("p99", 0), 3)),
    ]
    width = max(len(name) for name, _, _ in rows) + 2
    print(f"\n{'metric'.ljust(width)} {before:>14}  {after:>14}")
    print("-" * (width + 32))
    for name, x, y in rows:
        print(f"{name.ljust(width)} {str(x):>14}  {str(y):>14}")

    # Also compute throughput delta (reads/s effective) - the topic guide
    # asks for the throughput cost of the fix.
    da = a.get("reads", 0) / max(1, a.get("duration_s", 1))
    db = b.get("reads", 0) / max(1, b.get("duration_s", 1))
    print(f"\nthroughput  {da:>14.1f}  {db:>14.1f}  reads/s effective")

    print()
    if (a.get("violation_rate", 0) or 0) > 0 and (b.get("violation_rate", 0) or 0) == 0:
        print(f"DECISION: {after!r} eliminated read-after-write violations vs {before!r}.")
    elif (b.get("violation_rate", 0) or 0) <= (a.get("violation_rate", 0) or 0):
        print(f"DECISION: {after!r} reduces violations relative to {before!r}.")
    else:
        print(f"DECISION: {after!r} did NOT reduce violations vs {before!r}; investigate before claiming a fix.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
