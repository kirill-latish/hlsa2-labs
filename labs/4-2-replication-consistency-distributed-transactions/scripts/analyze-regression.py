#!/usr/bin/env python3
"""Median + sigma + delta for the chosen regression candidate, with a
2-sigma decision: a delta is only "real" if |delta| > 2 * max(sigma).
"""
from __future__ import annotations

import json
import statistics
import sys
from pathlib import Path
from typing import Dict, List, Tuple


def load_runs(side_dir: Path) -> List[dict]:
    out: List[dict] = []
    for d in sorted(side_dir.iterdir()):
        if d.is_dir() and d.name.startswith("run"):
            p = d / "summary.json"
            if p.exists():
                out.append(json.loads(p.read_text()))
    return out


def metric(s: dict, candidate: str) -> Tuple[float, str]:
    """Return the metric we'll regress on for this candidate."""
    if candidate == "lsn-wait-on-raw":
        return s.get("violation_rate", 0.0), "violation_rate"
    if candidate == "replace-2pc-with-saga":
        return s.get("success_rate", 0.0), "success_rate"
    if candidate == "outbox-cdc":
        return s.get("latency_ms", {}).get("p99", 0.0), "p99_latency_ms"
    return s.get("success_rate", 0.0), "success_rate"


def median_sigma(xs: List[float]) -> Tuple[float, float]:
    if not xs:
        return 0.0, 0.0
    med = statistics.median(xs)
    sd = statistics.pstdev(xs) if len(xs) > 1 else 0.0
    return med, sd


def main(argv: List[str]) -> int:
    if len(argv) < 3:
        print(f"usage: {argv[0]} <perf/results/regression> <candidate>", file=sys.stderr)
        return 2
    base = Path(argv[1]) / argv[2]
    candidate = argv[2]
    if not base.is_dir():
        print(f"missing: {base}", file=sys.stderr)
        return 2

    base_dir = base / "baseline"
    cand_dir = base / "candidate"
    if not base_dir.is_dir() or not cand_dir.is_dir():
        print(f"need both {base_dir} and {cand_dir}", file=sys.stderr)
        return 2

    base_runs = load_runs(base_dir)
    cand_runs = load_runs(cand_dir)
    if not base_runs or not cand_runs:
        print("no runs to compare", file=sys.stderr)
        return 2

    metric_name = metric(base_runs[0], candidate)[1]
    base_metrics = [metric(r, candidate)[0] for r in base_runs]
    cand_metrics = [metric(r, candidate)[0] for r in cand_runs]

    bm, bs = median_sigma(base_metrics)
    cm, cs = median_sigma(cand_metrics)
    delta = cm - bm
    threshold = 2 * max(bs, cs)
    real = abs(delta) > threshold

    print(f"\nCandidate: {candidate}  (metric={metric_name})\n")
    print(f"  baseline:  median={bm:.6f}  sigma={bs:.6f}  n={len(base_metrics)}")
    print(f"  candidate: median={cm:.6f}  sigma={cs:.6f}  n={len(cand_metrics)}")
    print(f"  delta:     {delta:+.6f}    2*max(sigma)={threshold:.6f}")
    print(f"  decision:  {'REAL change' if real else 'within noise (2-sigma)'}")

    summary = {
        "candidate": candidate,
        "metric": metric_name,
        "baseline_median": bm, "baseline_sigma": bs, "baseline_n": len(base_metrics),
        "candidate_median": cm, "candidate_sigma": cs, "candidate_n": len(cand_metrics),
        "delta": delta, "threshold_2sigma": threshold, "real_change": real,
    }
    (base / "summary.json").write_text(json.dumps(summary, indent=2))
    print(f"\nWrote {base/'summary.json'}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
