#!/usr/bin/env python3
"""Analyze replication lag samples written by lag-sampler.

Each run dir contains samples.csv with columns:
  ts, replica, primary_lsn, replica_lsn, bytes_behind, seconds_behind

Prints p50/p95/p99/p99.9 across all samples per replica per run, and
the run-to-run sigma.
"""
from __future__ import annotations

import csv
import json
import math
import statistics
import sys
from pathlib import Path
from typing import Dict, List


def percentile(xs: List[float], p: float) -> float:
    if not xs:
        return 0.0
    s = sorted(xs)
    idx = int(round(p * (len(s) - 1)))
    return s[max(0, min(idx, len(s) - 1))]


def load_run(run_dir: Path) -> Dict[str, List[float]]:
    csv_path = run_dir / "samples.csv"
    if not csv_path.exists():
        return {}
    by_replica: Dict[str, List[float]] = {}
    with csv_path.open() as f:
        reader = csv.DictReader(f)
        for row in reader:
            try:
                secs = float(row["seconds_behind"])
            except (KeyError, ValueError):
                continue
            by_replica.setdefault(row["replica"], []).append(secs)
    return by_replica


def main(argv: List[str]) -> int:
    if len(argv) < 2:
        print(f"usage: {argv[0]} <perf/results/lag>", file=sys.stderr)
        return 2
    base = Path(argv[1])
    if not base.is_dir():
        print(f"missing directory: {base}", file=sys.stderr)
        return 2

    runs = sorted([d for d in base.iterdir() if d.is_dir() and d.name.startswith("run")])
    if not runs:
        print(f"no run* directories under {base}", file=sys.stderr)
        return 2

    summary: Dict[str, Dict[str, Dict[str, float]]] = {}

    print(f"\nLag distribution across {len(runs)} runs (seconds)\n")
    for run_dir in runs:
        by_replica = load_run(run_dir)
        run_summary: Dict[str, Dict[str, float]] = {}
        for replica, samples in sorted(by_replica.items()):
            stats = {
                "samples": len(samples),
                "p50": percentile(samples, 0.50),
                "p95": percentile(samples, 0.95),
                "p99": percentile(samples, 0.99),
                "p999": percentile(samples, 0.999),
                "max": max(samples) if samples else 0.0,
            }
            run_summary[replica] = stats
            print(
                f"{run_dir.name:8s} {replica:10s} "
                f"n={stats['samples']:>5} "
                f"p50={stats['p50']*1000:7.2f}ms "
                f"p95={stats['p95']*1000:7.2f}ms "
                f"p99={stats['p99']*1000:7.2f}ms "
                f"p999={stats['p999']*1000:7.2f}ms "
                f"max={stats['max']*1000:7.2f}ms"
            )
        summary[run_dir.name] = run_summary

    # Run-to-run sigma per replica per percentile.
    print("\nRun-to-run sigma (ms) at each percentile\n")
    replicas: set = set()
    for s in summary.values():
        replicas.update(s.keys())
    for replica in sorted(replicas):
        for pct in ("p50", "p95", "p99", "p999"):
            xs = [summary[r][replica][pct] * 1000 for r in summary if replica in summary[r]]
            if len(xs) > 1:
                sd = statistics.pstdev(xs)
            else:
                sd = 0.0
            print(f"{replica:10s} {pct:5s} sigma={sd:7.2f} ms (median={statistics.median(xs):7.2f} ms)")

    # Write a machine-readable summary alongside the runs.
    out = base / "summary.json"
    with out.open("w") as f:
        json.dump({"runs": summary}, f, indent=2)
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
