#!/usr/bin/env python3
# CDC-lag percentile analyzer. Walks perf/results/cdc-lag/<label>/run-*/lag_samples.csv
# and prints p50/p95/p99/p99.9 per label, then a base vs 2x compare.
from __future__ import annotations

import csv
import statistics
import sys
from pathlib import Path


def read_lags(label_dir: Path) -> list[int]:
    lags = []
    for run_dir in sorted(label_dir.glob("run-*")):
        path = run_dir / "lag_samples.csv"
        if not path.exists():
            continue
        with path.open() as f:
            reader = csv.DictReader(f)
            for row in reader:
                if row.get("outcome") != "ok":
                    continue
                try:
                    lags.append(int(row["lag_ms"]))
                except (KeyError, ValueError):
                    continue
    return lags


def percentile(sorted_values: list[int], q: float) -> float:
    if not sorted_values:
        return float("nan")
    if q <= 0:
        return float(sorted_values[0])
    if q >= 100:
        return float(sorted_values[-1])
    idx = (q / 100.0) * (len(sorted_values) - 1)
    lo = int(idx)
    hi = min(lo + 1, len(sorted_values) - 1)
    frac = idx - lo
    return sorted_values[lo] + (sorted_values[hi] - sorted_values[lo]) * frac


def summarize(label_dir: Path):
    lags = sorted(read_lags(label_dir))
    if not lags:
        print(f"[{label_dir.name}] no samples")
        return None
    p50 = percentile(lags, 50)
    p95 = percentile(lags, 95)
    p99 = percentile(lags, 99)
    p999 = percentile(lags, 99.9)
    print(
        f"[{label_dir.name:>10}] n={len(lags):>6}  "
        f"p50={p50:.0f}  p95={p95:.0f}  p99={p99:.0f}  p99.9={p999:.0f}  "
        f"max={lags[-1]}  mean={statistics.mean(lags):.0f}"
    )
    return {"label": label_dir.name, "p50": p50, "p95": p95, "p99": p99, "p999": p999, "n": len(lags)}


def main():
    if len(sys.argv) < 2:
        print("usage: analyze-cdc-lag.py <perf/results/cdc-lag>", file=sys.stderr)
        sys.exit(2)
    base = Path(sys.argv[1])
    if not base.exists():
        print(f"no results under {base}", file=sys.stderr)
        sys.exit(1)
    summaries = []
    for label_dir in sorted(d for d in base.iterdir() if d.is_dir()):
        s = summarize(label_dir)
        if s:
            summaries.append(s)
    base_s = next((s for s in summaries if s["label"] in ("base", "1x")), None)
    twox_s = next((s for s in summaries if s["label"] in ("2x",)), None)
    if base_s and twox_s:
        print()
        print("=== base vs 2x ===")
        for k in ("p50", "p95", "p99", "p999"):
            ratio = twox_s[k] / base_s[k] if base_s[k] else float("nan")
            print(f"  {k:<6}: {base_s[k]:.0f} -> {twox_s[k]:.0f}  (x{ratio:.2f})")


if __name__ == "__main__":
    main()
