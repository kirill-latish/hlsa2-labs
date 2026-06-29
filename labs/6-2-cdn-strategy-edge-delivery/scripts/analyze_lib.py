"""analyze_lib.py - shared Prometheus text parsing + per-run delta helpers
for the lab-6-2 analyze-* scripts.

The bench scripts snapshot each node's /metrics BEFORE and AFTER a run
(suffix "before"/"after"), so every figure here is a per-run delta of
cumulative counters rather than a since-boot total. Gauges (cache
entries) are read from the AFTER snapshot.

Import-name uses underscores so it's a valid module identifier; the CLI
scripts (analyze-baseline.py, ...) stay dash-named.
"""

from __future__ import annotations

import math
import re
from pathlib import Path

METRIC_RE = re.compile(r"^([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\}\s+([0-9eE.+\-]+)\s*$")
BARE_RE = re.compile(r"^([a-zA-Z_:][a-zA-Z0-9_:]*)\s+([0-9eE.+\-]+)\s*$")

POPS = ("pop-1", "pop-2", "pop-3")
CACHE_STATUSES = ("HIT", "MISS", "EXPIRED", "STALE", "BYPASS")


def parse_prom(text: str) -> dict[str, list[tuple[dict[str, str], float]]]:
    """Tiny Prometheus text-format parser -> {metric: [(labels, value), ...]}."""
    out: dict[str, list[tuple[dict[str, str], float]]] = {}
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        m = METRIC_RE.match(line)
        if m:
            name, labels_str, value = m.groups()
            labels = {
                kv[0]: kv[1]
                for kv in re.findall(r'([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"', labels_str)
            }
            try:
                out.setdefault(name, []).append((labels, float(value)))
            except ValueError:
                pass
            continue
        b = BARE_RE.match(line)
        if b:
            try:
                out.setdefault(b.group(1), []).append(({}, float(b.group(2))))
            except ValueError:
                pass
    return out


def load(path: Path) -> dict:
    if not path.exists():
        return {}
    return parse_prom(path.read_text())


def series_sum(parsed: dict, name: str, **want: str) -> float:
    """Sum the values of `name` whose labels match every key=value in want."""
    total = 0.0
    for labels, val in parsed.get(name, []):
        if all(labels.get(k) == v for k, v in want.items()):
            total += val
    return total


def series_by(parsed: dict, name: str, key: str, **want: str) -> dict[str, float]:
    """Group-sum the values of `name` by one label key, filtered by want."""
    out: dict[str, float] = {}
    for labels, val in parsed.get(name, []):
        if all(labels.get(k) == v for k, v in want.items()):
            out[labels.get(key, "")] = out.get(labels.get(key, ""), 0.0) + val
    return out


def delta(before: dict, after: dict, name: str, **want: str) -> float:
    return series_sum(after, name, **want) - series_sum(before, name, **want)


def _node_path(run_dir: Path, node: str, suffix: str) -> Path:
    return run_dir / f"{node}-metrics-{suffix}.txt"


def edge_cache_delta(run_dir: Path) -> dict[str, float]:
    """Per-run cache-response counts by status, summed across all PoPs."""
    out = {s: 0.0 for s in CACHE_STATUSES}
    for node in POPS:
        before = load(_node_path(run_dir, node, "before"))
        after = load(_node_path(run_dir, node, "after"))
        for s in CACHE_STATUSES:
            out[s] += delta(before, after, "lab62_cache_responses_total", role="pop", status=s)
    return out


def edge_bytes_delta(run_dir: Path) -> dict[str, float]:
    """Per-run bytes served, by source (edge|origin), summed across PoPs."""
    out = {"edge": 0.0, "origin": 0.0}
    for node in POPS:
        before = load(_node_path(run_dir, node, "before"))
        after = load(_node_path(run_dir, node, "after"))
        for src in ("edge", "origin"):
            out[src] += delta(before, after, "lab62_bytes_served_total", source=src)
    return out


def origin_requests_delta(run_dir: Path) -> float:
    before = load(_node_path(run_dir, "origin", "before"))
    after = load(_node_path(run_dir, "origin", "after"))
    return delta(before, after, "lab62_origin_requests_total")


def origin_object_delta(run_dir: Path, obj: str) -> float:
    before = load(_node_path(run_dir, "origin", "before"))
    after = load(_node_path(run_dir, "origin", "after"))
    return delta(before, after, "lab62_origin_object_requests_total", object=obj)


def edge_entries_after(run_dir: Path) -> float:
    """Cache-entry cardinality across PoPs (gauge, read from AFTER)."""
    total = 0.0
    for node in POPS:
        after = load(_node_path(run_dir, node, "after"))
        total += series_sum(after, "lab62_cache_entries", node=node)
    return total


def run_dirs(base: Path) -> list[Path]:
    if not base.exists():
        return []
    out = sorted(d for d in base.iterdir() if d.is_dir() and d.name.startswith("run"))
    if not out and (base / "origin-metrics-after.txt").exists():
        return [base]
    return out


def safe_ratio(num: float, den: float) -> float:
    return num / den if den > 0 else math.nan


def fmt_pct(v: float) -> str:
    if v is None or (isinstance(v, float) and math.isnan(v)):
        return "n/a"
    return f"{v * 100:.2f}%"
