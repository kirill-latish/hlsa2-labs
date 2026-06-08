#!/usr/bin/env python3
# BEFORE/AFTER side-by-side comparison with the 2-sigma decision rule
# (lifted from labs 3-3 + 4-2).
from __future__ import annotations

import json
import os
import statistics
import sys
from pathlib import Path


def load_runs(label_dir: Path):
    runs = []
    for run_dir in sorted(label_dir.glob("run-*")):
        path = run_dir / "partition_metrics.json"
        if not path.exists():
            continue
        with path.open() as f:
            runs.append(json.load(f))
    return runs


def stats_of(runs: list):
    ratios = [r.get("max_to_mean", 0.0) for r in runs]
    return statistics.mean(ratios), statistics.pstdev(ratios), len(ratios)


def main():
    if len(sys.argv) < 2:
        print("usage: compare-skew.py <perf/results/skew>", file=sys.stderr)
        sys.exit(2)
    base = Path(sys.argv[1])
    before_label = os.environ.get("BEFORE")
    after_label = os.environ.get("AFTER")
    if not before_label or not after_label:
        print("BEFORE and AFTER env vars are required", file=sys.stderr)
        sys.exit(2)

    before_runs = load_runs(base / before_label)
    after_runs = load_runs(base / after_label)
    if not before_runs or not after_runs:
        print(f"no runs found for one of the labels (before={len(before_runs)}, after={len(after_runs)})",
              file=sys.stderr)
        sys.exit(1)

    bm, bs, bn = stats_of(before_runs)
    am, asd, an = stats_of(after_runs)

    print(f"=== compare {before_label} -> {after_label} ===")
    print(f"  {before_label}: max/mean = {bm:.3f} ± {bs:.3f}  (n={bn})")
    print(f"  {after_label}:  max/mean = {am:.3f} ± {asd:.3f}  (n={an})")

    delta = bm - am
    sigma = max(bs, asd, 1e-9)
    n_sigma = delta / sigma
    print(f"  delta:        {delta:.3f}")
    print(f"  sigma_max:    {sigma:.3f}")
    print(f"  delta_sigmas: {n_sigma:.2f}")
    if n_sigma >= 2.0 and am < bm:
        print("  decision:    APPLY (2-sigma significant improvement)")
    elif n_sigma <= -2.0:
        print("  decision:    REGRESSION (2-sigma)")
    else:
        print("  decision:    NEUTRAL (within 2 sigma)")


if __name__ == "__main__":
    main()
