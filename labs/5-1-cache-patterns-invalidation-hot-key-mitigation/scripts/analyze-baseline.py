#!/usr/bin/env python3
"""analyze-baseline.py - aggregate hit ratio + latency + source-fetch
rate + evictions across the 3-run baseline, with run-to-run sigma.

Counters are diffed between app-metrics-before.txt and app-metrics.txt
so each run reflects only its own traffic. Latency percentiles come
from the app's lab51_cache_read_duration_seconds histogram.

Writes <baseline-dir>/report.md and prints it.

Usage:
    python3 scripts/analyze-baseline.py perf/results/baseline
"""

from __future__ import annotations

import json
import math
import sys
from pathlib import Path
from statistics import median, pstdev

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from prom_lib import load, counter_sum, delta_counter, read_percentiles  # noqa: E402


def run_dirs(base: Path):
    if not base.exists():
        return []
    return sorted(d for d in base.iterdir() if d.is_dir() and d.name.startswith("run"))


def summarize_run(run_dir: Path) -> dict:
    before = load(run_dir / "app-metrics-before.txt")
    after = load(run_dir / "app-metrics.txt")
    meta = {}
    mp = run_dir / "meta.json"
    if mp.exists():
        meta = json.loads(mp.read_text())
    dur = max(float(meta.get("duration_s", 1)), 1.0)

    reads = delta_counter(before, after, "lab51_cache_requests_total")
    misses_out = delta_counter(before, after, "lab51_cache_requests_total", {"outcome": "miss"})
    hits = reads - misses_out
    source_fetches = delta_counter(before, after, "lab51_cache_source_fetches_total")
    evictions = delta_counter(before, after, "lab51_cache_evictions_total")

    pct = read_percentiles(after, "lab51_cache_read_duration_seconds_bucket", [0.50, 0.99])

    summary = {}
    sp = run_dir / "summary.json"
    if sp.exists():
        summary = json.loads(sp.read_text())

    return {
        "run": run_dir.name,
        "reads": int(reads),
        "offered": summary.get("offered", 0),
        "served": summary.get("served", 0),
        "hit_ratio": (hits / reads) if reads > 0 else math.nan,
        "source_fetch_rate": source_fetches / dur,
        "evictions_per_sec": evictions / dur,
        "p50": pct[0.50],
        "p99": pct[0.99],
    }


def fmt(v, unit=""):
    if v is None or (isinstance(v, float) and math.isnan(v)):
        return "   n/a"
    return f"{v:8.3f}{unit}"


def agg(rows, key):
    vals = [r[key] for r in rows if not (isinstance(r[key], float) and math.isnan(r[key]))]
    if not vals:
        return (math.nan, math.nan)
    return (median(vals), pstdev(vals) if len(vals) > 1 else 0.0)


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    runs = run_dirs(base)
    if not runs:
        print(f"ERROR: no run* subdirs under {base}", file=sys.stderr)
        return 3
    rows = [summarize_run(r) for r in runs]

    lines = [f"# Baseline analysis - {base.name}\n", f"_{len(rows)} runs_\n"]
    lines.append("\n| run | reads | offered | served | hit_ratio | source_fetch/s | evict/s | p50 ms | p99 ms |")
    lines.append("|-----|-------|---------|--------|-----------|----------------|---------|--------|--------|")
    for r in rows:
        hr = "   n/a" if math.isnan(r["hit_ratio"]) else f"{r['hit_ratio']*100:6.2f}%"
        lines.append(
            f"| {r['run']} | {r['reads']} | {r['offered']} | {r['served']} | {hr} | "
            f"{fmt(r['source_fetch_rate'])} | {fmt(r['evictions_per_sec'])} | "
            f"{fmt(r['p50'])} | {fmt(r['p99'])} |"
        )
    lines.append("\n## Aggregated (median, sigma)\n")
    for key, label, unit in [
        ("hit_ratio", "hit_ratio", ""),
        ("source_fetch_rate", "source_fetch_rate", " /s"),
        ("evictions_per_sec", "evictions_per_sec", " /s"),
        ("p50", "p50", " ms"),
        ("p99", "p99", " ms"),
    ]:
        med, sig = agg(rows, key)
        lines.append(f"- **{label}** median={fmt(med, unit)} sigma={fmt(sig, unit)}")

    out_md = base / "report.md"
    out_md.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out_md}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
