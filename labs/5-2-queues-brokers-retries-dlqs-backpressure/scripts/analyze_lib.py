"""analyze_lib.py - shared parsing + summarization for the analyze-*
scripts. Import-name uses underscores so it's a valid module identifier;
the CLI scripts (analyze-baseline.py, ...) remain dash-named.

Each run dir has:
  summary.json        loadgen/producer snapshot (produced, produce_errors)
  metrics-before.txt  cumulative Prometheus counters before the run
  metrics.txt         cumulative Prometheus counters after the run
  meta.json           run parameters

Counters are reported as (after - before) deltas; gauges use the after
value; histogram percentiles use the delta buckets.
"""

from __future__ import annotations

import json
import math
import re
from pathlib import Path

METRIC_RE = re.compile(r'^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{([^}]*)\})?\s+([0-9eE.+\-]+)\s*$')


def parse_prom(text: str) -> dict[str, list[tuple[dict[str, str], float]]]:
    out: dict[str, list[tuple[dict[str, str], float]]] = {}
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        m = METRIC_RE.match(line)
        if not m:
            continue
        name, labels_str, value = m.groups()
        labels: dict[str, str] = {}
        if labels_str:
            for kv in re.findall(r'([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"', labels_str):
                labels[kv[0]] = kv[1]
        try:
            out.setdefault(name, []).append((labels, float(value)))
        except ValueError:
            pass
    return out


def _read(run_dir: Path, name: str) -> dict:
    p = run_dir / name
    if not p.exists():
        return {}
    return parse_prom(p.read_text())


def counter_total(metric: str, run_dir: Path, label_filter: dict | None = None) -> float:
    """Sum a counter across label sets, after-minus-before delta."""
    after = sum(_match(metric, _read(run_dir, "metrics.txt"), label_filter))
    before = sum(_match(metric, _read(run_dir, "metrics-before.txt"), label_filter))
    delta = after - before
    return delta if delta >= 0 else after


def _match(metric: str, parsed: dict, label_filter: dict | None):
    for labels, val in parsed.get(metric, []):
        if label_filter and any(labels.get(k) != v for k, v in label_filter.items()):
            continue
        yield val


def counter_by_label(metric: str, run_dir: Path, key: str) -> dict[str, float]:
    after = _read(run_dir, "metrics.txt")
    before = _read(run_dir, "metrics-before.txt")
    bmap: dict[str, float] = {}
    for labels, val in before.get(metric, []):
        bmap[labels.get(key, "?")] = bmap.get(labels.get(key, "?"), 0.0) + val
    out: dict[str, float] = {}
    for labels, val in after.get(metric, []):
        k = labels.get(key, "?")
        out[k] = out.get(k, 0.0) + val
    for k in out:
        out[k] = max(out[k] - bmap.get(k, 0.0), 0.0)
    return out


def gauge_max(metric: str, run_dir: Path) -> float:
    vals = [v for _, v in _read(run_dir, "metrics.txt").get(metric, [])]
    return max(vals) if vals else math.nan


def gauge_value(metric: str, run_dir: Path) -> float:
    vals = [v for _, v in _read(run_dir, "metrics.txt").get(metric, [])]
    return vals[0] if vals else math.nan


def histogram_percentile(samples, q: float) -> float:
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
            bucket = count - prev_count
            if bucket <= 0:
                return le
            frac = (target - prev_count) / bucket
            return prev_le + (le - prev_le) * frac
        prev_le, prev_count = le, count
    return samples[-1][0]


def processing_percentiles(run_dir: Path) -> dict:
    """p50/p99 (ms) from the processing-latency histogram, delta buckets."""
    after = _read(run_dir, "metrics.txt")
    before = _read(run_dir, "metrics-before.txt")
    metric = "lab52_processing_duration_seconds_bucket"

    def collect(parsed):
        by_le: dict[float, float] = {}
        for labels, val in parsed.get(metric, []):
            le = labels.get("le", "+Inf")
            le_f = math.inf if le == "+Inf" else float(le)
            by_le[le_f] = by_le.get(le_f, 0.0) + val
        return by_le

    a = collect(after)
    b = collect(before)
    by_le = {le: max(a.get(le, 0.0) - b.get(le, 0.0), 0.0) for le in a}
    if not any(by_le.values()):
        by_le = a  # fall back to cumulative if no delta available
    samples = sorted(by_le.items(), key=lambda x: x[0])
    return {
        "p50": histogram_percentile(samples, 0.50) * 1000,
        "p99": histogram_percentile(samples, 0.99) * 1000,
    }


def run_dirs(base: Path):
    if not base.exists():
        return []
    out = sorted(d for d in base.iterdir() if d.is_dir() and d.name.startswith("run"))
    if not out and (base / "metrics.txt").exists():
        return [base]
    return out


def summarize_run(run_dir: Path) -> dict:
    sj = run_dir / "summary.json"
    summary = json.loads(sj.read_text()) if sj.exists() else {}
    meta = run_dir / "meta.json"
    m = json.loads(meta.read_text()) if meta.exists() else {}
    dur = max(int(m.get("duration_s", summary.get("duration_s", 1)) or 1), 1)

    acked = counter_total("lab52_messages_acked_total", run_dir)
    retries = counter_total("lab52_retries_total", run_dir)
    dlq = counter_total("lab52_dlq_total", run_dir)
    produced = counter_total("lab52_messages_produced_total", run_dir)
    prod_errs = counter_total("lab52_producer_errors_total", run_dir)
    pct = processing_percentiles(run_dir)

    return {
        "run": run_dir.name,
        "duration_s": dur,
        "produced": produced if produced > 0 else float(summary.get("produced", 0)),
        "produce_errors": prod_errs if prod_errs > 0 else float(summary.get("produce_errors", 0)),
        "acked": acked,
        "throughput_rps": acked / dur,
        "retries": retries,
        "retry_rps": retries / dur,
        "dlq": dlq,
        "dlq_rps": dlq / dur,
        "lag_count": gauge_value("lab52_consumer_lag_count", run_dir),
        "lag_age_s": gauge_max("lab52_oldest_unprocessed_age_seconds", run_dir),
        "p50": pct["p50"],
        "p99": pct["p99"],
    }
