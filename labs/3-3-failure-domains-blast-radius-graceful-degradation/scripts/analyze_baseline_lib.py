"""analyze_baseline_lib.py - shared parsing + summarization for the
analyze-* scripts. Import-name uses underscores so it's a valid module
identifier; the CLI scripts (analyze-baseline.py, analyze-compare.py)
remain dash-named.
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
    """samples: sorted list of (le, cumulative_count). Returns approximate
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


def gateway_percentiles(metrics_path: Path) -> dict:
    """Pulls p50/p95/p99/p99.9 (ms) from the gateway's /checkout histogram, 2xx only."""
    if not metrics_path.exists():
        return {p: math.nan for p in ("p50", "p95", "p99", "p999")}
    parsed = parse_prom(metrics_path.read_text())
    series = parsed.get("lab33_http_request_duration_seconds_bucket", [])
    by_le: dict[float, float] = {}
    for labels, val in series:
        if labels.get("service") != "gateway":
            continue
        if labels.get("endpoint") != "/checkout":
            continue
        code = labels.get("code", "")
        if not code.startswith("2"):
            continue
        le = labels.get("le", "+Inf")
        le_f = math.inf if le == "+Inf" else float(le)
        by_le[le_f] = by_le.get(le_f, 0.0) + val
    samples = sorted(by_le.items(), key=lambda x: x[0])
    return {
        "p50": histogram_percentile(samples, 0.50) * 1000,
        "p95": histogram_percentile(samples, 0.95) * 1000,
        "p99": histogram_percentile(samples, 0.99) * 1000,
        "p999": histogram_percentile(samples, 0.999) * 1000,
    }


def journey_rates(metrics_path: Path) -> dict:
    out = {"success_full": 0.0, "success_degraded": 0.0, "failed": 0.0, "shed": 0.0}
    if not metrics_path.exists():
        return out
    parsed = parse_prom(metrics_path.read_text())
    for labels, val in parsed.get("lab33_gateway_checkout_total", []):
        outcome = labels.get("outcome")
        if outcome in out:
            out[outcome] += val
    return out


def run_dirs(base: Path):
    if not base.exists():
        return []
    out = sorted(d for d in base.iterdir() if d.is_dir() and d.name.startswith("run"))
    if not out and (base / "gateway-metrics.txt").exists():
        return [base]
    return out


def summarize_run(run_dir: Path) -> dict:
    sj = run_dir / "summary.json"
    summary = json.loads(sj.read_text()) if sj.exists() else {}
    metrics = run_dir / "gateway-metrics.txt"
    pct = gateway_percentiles(metrics)
    journey = journey_rates(metrics)
    total_checkouts = sum(journey.values())
    success_ratio = (
        (journey["success_full"] + journey["success_degraded"]) / total_checkouts
        if total_checkouts > 0
        else math.nan
    )
    return {
        "run": run_dir.name,
        "offered": summary.get("offered", 0),
        "served": summary.get("served", 0),
        "failed": summary.get("failed", 0),
        "loadgen_success_ratio": summary.get("served", 0) / max(summary.get("offered", 0), 1),
        "gateway_success_full": int(journey["success_full"]),
        "gateway_success_degraded": int(journey["success_degraded"]),
        "gateway_failed": int(journey["failed"]),
        "gateway_shed": int(journey["shed"]),
        "gateway_success_ratio": success_ratio,
        **pct,
    }


# Hint: dep-level helpers used by analyze-blast-radius.py.
def per_dep_breakdown(metrics_path: Path) -> dict:
    if not metrics_path.exists():
        return {}
    parsed = parse_prom(metrics_path.read_text())
    dep_calls: dict[tuple[str, str, str], float] = defaultdict(float)
    for labels, val in parsed.get("lab33_gateway_dep_calls_total", []):
        key = (labels.get("dep", "?"), labels.get("critical", "?"), labels.get("outcome", "?"))
        dep_calls[key] += val
    per_dep: dict[str, dict] = defaultdict(lambda: {"total": 0.0, "success": 0.0, "critical": "false"})
    for (dep, crit, outcome), v in dep_calls.items():
        per_dep[dep]["total"] += v
        per_dep[dep]["critical"] = crit
        if outcome == "success":
            per_dep[dep]["success"] += v
    return dict(per_dep)
