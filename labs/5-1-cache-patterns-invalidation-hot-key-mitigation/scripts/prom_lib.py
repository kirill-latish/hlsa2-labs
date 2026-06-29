"""prom_lib.py - shared Prometheus text-format parsing for the lab 5-1
analyze-* scripts. Import-name uses underscores so it's a valid module
identifier; the CLI scripts remain dash-named.

stdlib only.
"""

from __future__ import annotations

import math
import re
from pathlib import Path

METRIC_RE = re.compile(r'^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{([^}]*)\})?\s+([0-9eE.+\-]+)\s*$')


def parse_prom(text: str) -> dict[str, list[tuple[dict[str, str], float]]]:
    """Returns {metric: [(labels, value), ...]}."""
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


def load(path: Path) -> dict[str, list[tuple[dict[str, str], float]]]:
    if not path.exists():
        return {}
    return parse_prom(path.read_text())


def counter_sum(parsed, name: str, match: dict[str, str] | None = None) -> float:
    """Sum a metric's series, optionally filtered to label matches."""
    total = 0.0
    for labels, val in parsed.get(name, []):
        if match and any(labels.get(k) != v for k, v in match.items()):
            continue
        total += val
    return total


def counter_by_label(parsed, name: str, label: str) -> dict[str, float]:
    """Group a metric's value by one label key."""
    out: dict[str, float] = {}
    for labels, val in parsed.get(name, []):
        out[labels.get(label, "?")] = out.get(labels.get(label, "?"), 0.0) + val
    return out


def delta_counter(before, after, name: str, match: dict[str, str] | None = None) -> float:
    """after-before for a (possibly label-filtered) counter total."""
    return counter_sum(after, name, match) - counter_sum(before, name, match)


def histogram_percentile(samples: list[tuple[float, float]], q: float) -> float:
    """samples: list of (le, cumulative_count). Approx percentile q."""
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


def read_percentiles(parsed, bucket_metric: str, q_list, match: dict[str, str] | None = None) -> dict:
    """Pull percentiles (ms) from a *_bucket histogram, summing buckets
    across all matching series by le."""
    by_le: dict[float, float] = {}
    for labels, val in parsed.get(bucket_metric, []):
        if match and any(labels.get(k) != v for k, v in match.items()):
            continue
        le = labels.get("le", "+Inf")
        le_f = math.inf if le == "+Inf" else float(le)
        by_le[le_f] = by_le.get(le_f, 0.0) + val
    samples = sorted(by_le.items(), key=lambda x: x[0])
    return {q: histogram_percentile(samples, q) * 1000 for q in q_list}
