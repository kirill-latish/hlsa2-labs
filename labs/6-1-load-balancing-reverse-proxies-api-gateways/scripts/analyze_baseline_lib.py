"""analyze_baseline_lib.py - shared parsing + summarization for the
lab 6-1 analyze-* scripts. Import-name uses underscores so it's a valid
module identifier; the CLI scripts (analyze-baseline.py, ...) stay
dash-named.

Everything keys off the edge-proxy's Prometheus textfile snapshot
(edge-metrics.txt) plus the loadgen /summary snapshot (summary.json).
"""

from __future__ import annotations

import json
import math
import re
from collections import defaultdict
from pathlib import Path

METRIC_RE = re.compile(r'^([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\}\s+([0-9eE.+\-]+)\s*$')


def parse_prom(text: str):
    """Tiny Prometheus text-format parser. Returns {metric: [(labels, value), ...]}."""
    out: dict[str, list[tuple[dict[str, str], float]]] = {}
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        m = METRIC_RE.match(line)
        if not m:
            parts = line.split()
            if len(parts) == 2:
                try:
                    out.setdefault(parts[0], []).append(({}, float(parts[1])))
                except ValueError:
                    pass
            continue
        name, labels_str, value = m.groups()
        labels = {}
        for kv in re.findall(r'([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"', labels_str):
            labels[kv[0]] = kv[1]
        try:
            out.setdefault(name, []).append((labels, float(value)))
        except ValueError:
            pass
    return out


def histogram_percentile(samples, q: float) -> float:
    """samples: list of (le, cumulative_count). Returns approximate
    percentile q (0<q<1), mirroring histogram_quantile()."""
    samples = sorted(samples, key=lambda x: x[0])
    if not samples:
        return math.nan
    total = samples[-1][1]
    if total <= 0:
        return math.nan
    target = q * total
    prev_le, prev_count = 0.0, 0.0
    for le, count in samples:
        if count >= target:
            if le == math.inf or le > 1e9:
                return prev_le if prev_le > 0 else math.nan
            bucket_size = count - prev_count
            if bucket_size <= 0:
                return le
            frac = (target - prev_count) / bucket_size
            return prev_le + (le - prev_le) * frac
        prev_le, prev_count = le, count
    return samples[-1][0]


def _buckets(parsed, metric: str, label_filter=None) -> list[tuple[float, float]]:
    by_le: dict[float, float] = {}
    for labels, val in parsed.get(metric, []):
        if label_filter and not label_filter(labels):
            continue
        le = labels.get("le", "+Inf")
        le_f = math.inf if le == "+Inf" else float(le)
        by_le[le_f] = by_le.get(le_f, 0.0) + val
    return sorted(by_le.items(), key=lambda x: x[0])


def edge_percentiles(metrics_path: Path) -> dict:
    """p50/p99 (ms) for BOTH the edge overhead and the total request
    duration. Overhead is the headline number the lab isolates."""
    keys = ("overhead_p50", "overhead_p99", "total_p50", "total_p99")
    if not metrics_path.exists():
        return {k: math.nan for k in keys}
    parsed = parse_prom(metrics_path.read_text())
    over = _buckets(parsed, "lab61_edge_overhead_seconds_bucket")
    total = _buckets(parsed, "lab61_edge_request_duration_seconds_bucket",
                     lambda l: l.get("code", "").startswith("2"))
    return {
        "overhead_p50": histogram_percentile(over, 0.50) * 1000,
        "overhead_p99": histogram_percentile(over, 0.99) * 1000,
        "total_p50": histogram_percentile(total, 0.50) * 1000,
        "total_p99": histogram_percentile(total, 0.99) * 1000,
    }


def backend_distribution(metrics_path: Path) -> dict[str, float]:
    """Per-backend request counts from lab61_edge_backend_requests_total."""
    if not metrics_path.exists():
        return {}
    parsed = parse_prom(metrics_path.read_text())
    out: dict[str, float] = defaultdict(float)
    for labels, val in parsed.get("lab61_edge_backend_requests_total", []):
        out[labels.get("backend", "?")] += val
    return dict(out)


def fivexx_counts(metrics_path: Path) -> dict[str, float]:
    """Edge 5xx counts by class (502/503/504/...) from lab61_edge_5xx_total."""
    if not metrics_path.exists():
        return {}
    parsed = parse_prom(metrics_path.read_text())
    out: dict[str, float] = defaultdict(float)
    for labels, val in parsed.get("lab61_edge_5xx_total", []):
        out[labels.get("code", "?")] += val
    return dict(out)


def max_vs_mean(counts: dict[str, float]) -> float:
    vals = [v for v in counts.values() if v > 0]
    if not vals:
        return math.nan
    mean = sum(vals) / len(vals)
    return max(vals) / mean if mean > 0 else math.nan


def run_dirs(base: Path):
    if not base.exists():
        return []
    out = sorted(d for d in base.iterdir() if d.is_dir() and d.name.startswith("run"))
    if not out and (base / "edge-metrics.txt").exists():
        return [base]
    return out


def summarize_run(run_dir: Path) -> dict:
    sj = run_dir / "summary.json"
    summary = json.loads(sj.read_text()) if sj.exists() else {}
    metrics = run_dir / "edge-metrics.txt"
    pct = edge_percentiles(metrics)
    offered = summary.get("offered", 0)
    served = summary.get("served", 0)
    dur = summary.get("duration_s", 0) or 0
    throughput = (served / dur) if dur else math.nan
    return {
        "run": run_dir.name,
        "offered": offered,
        "served": served,
        "failed": summary.get("failed", 0),
        "throughput_rps": throughput,
        "success_ratio": (served / offered) if offered else math.nan,
        **pct,
    }
