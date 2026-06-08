#!/usr/bin/env python3
# Reads partition_metrics.json files from perf/results/skew/<label>/run-*/
# and prints max/mean ratio + sigma per shard.
from __future__ import annotations

import json
import math
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


def summarize(label: str, runs: list):
    if not runs:
        print(f"[{label}] no runs found")
        return
    print(f"\n=== {label} ({len(runs)} runs) ===")
    print(f"  shard_key:   {runs[0].get('shard_key')}")
    print(f"  collection:  {runs[0].get('collection')}")
    print(f"  write_rate:  {runs[0].get('write_rate')}")
    print(f"  duration:    {runs[0].get('duration')}")

    ratios = [r.get("max_to_mean", 0.0) for r in runs]
    totals = [r.get("cluster_total", 0) for r in runs]
    print(f"  total writes per run: {totals}")
    print(f"  max/mean ratio:")
    print(f"    median: {statistics.median(ratios):.3f}")
    print(f"    mean:   {statistics.mean(ratios):.3f}")
    print(f"    sigma:  {statistics.pstdev(ratios):.3f}")

    # Aggregate per-shard counts
    shards: dict[str, list[int]] = {}
    for r in runs:
        for shard, count in (r.get("doc_count") or {}).items():
            shards.setdefault(shard, []).append(count)
    print("  per-shard avg / max / share:")
    grand_total = sum(sum(v) for v in shards.values())
    for shard in sorted(shards):
        counts = shards[shard]
        avg = statistics.mean(counts)
        mx = max(counts)
        share = sum(counts) / grand_total if grand_total else 0
        print(f"    {shard:<28} avg={avg:>10.0f}  max={mx:>10}  share={share*100:.1f}%")


def main():
    if len(sys.argv) < 2:
        print("usage: analyze-skew.py <perf/results/skew>", file=sys.stderr)
        sys.exit(2)
    base = Path(sys.argv[1])
    label = os.environ.get("LABEL")
    if label:
        labels = [base / label]
    else:
        labels = [d for d in base.iterdir() if d.is_dir()] if base.exists() else []
    if not labels:
        print(f"no labels found under {base}", file=sys.stderr)
        sys.exit(1)
    for label_dir in sorted(labels):
        summarize(label_dir.name, load_runs(label_dir))


if __name__ == "__main__":
    main()
