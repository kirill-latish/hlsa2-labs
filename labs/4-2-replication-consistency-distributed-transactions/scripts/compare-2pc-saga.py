#!/usr/bin/env python3
"""Side-by-side comparison of 2PC vs saga under healthy + faulted
conditions. Reads aggregated summary.json files written by
aggregate-runs.py.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Dict


def maybe_load(p: Path) -> Dict | None:
    if not p.exists():
        return None
    try:
        return json.loads(p.read_text())
    except Exception:
        return None


def cell(d: Dict | None, *keys: str) -> str:
    if d is None:
        return "-"
    cur: object = d
    for k in keys:
        if not isinstance(cur, dict) or k not in cur:
            return "-"
        cur = cur[k]
    if isinstance(cur, float):
        if abs(cur) < 1:
            return f"{cur:.4f}"
        return f"{cur:.2f}"
    return str(cur)


def main() -> int:
    base = Path("perf/results")
    matrix = {
        "2pc/healthy":  maybe_load(base / "2pc"  / "healthy"  / "summary.json"),
        "2pc/faulted":  maybe_load(base / "2pc"  / "faulted"  / "summary.json"),
        "saga/healthy": maybe_load(base / "saga" / "healthy" / "summary.json"),
        "saga/faulted": maybe_load(base / "saga" / "faulted" / "summary.json"),
    }

    print("\n2PC vs saga: aggregated across runs (median / sigma)\n")
    print(f"{'metric':<22} {'2pc.healthy':>14} {'2pc.faulted':>14} {'saga.healthy':>14} {'saga.faulted':>14}")
    print("-" * 86)
    rows = [
        ("Runs",            ("runs",)),
        ("Requests total",  ("requests_total",)),
        ("Failed total",    ("failed_total",)),
        ("Compensated tot", ("compensated_total",)),
        ("Success rate med",("success_rate", "median")),
        ("Success rate sd", ("success_rate", "sigma")),
        ("p99 latency ms",  ("latency_p99_ms", "median")),
        ("p99 sigma",       ("latency_p99_ms", "sigma")),
        ("p99.9 latency ms",("latency_p999_ms", "median")),
    ]
    for name, keys in rows:
        print(
            f"{name:<22} "
            f"{cell(matrix['2pc/healthy'],  *keys):>14} "
            f"{cell(matrix['2pc/faulted'],  *keys):>14} "
            f"{cell(matrix['saga/healthy'], *keys):>14} "
            f"{cell(matrix['saga/faulted'], *keys):>14}"
        )

    if matrix["2pc/faulted"] and matrix["saga/faulted"]:
        ts = matrix["2pc/faulted"]["success_rate"]["median"]
        sg = matrix["saga/faulted"]["success_rate"]["median"]
        print()
        print(f"DECISION: under fault saga success={sg:.4f} vs 2PC success={ts:.4f} (delta={sg-ts:+.4f}).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
