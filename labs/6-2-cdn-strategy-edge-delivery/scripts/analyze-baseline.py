#!/usr/bin/env python3
"""analyze-baseline.py - aggregate the edge cache effectiveness across the
baseline runs, reporting the metric pair teams never separate:

  * hit ratio BY REQUEST  (how often the origin is spared)
  * hit ratio BY BYTES    (how much bandwidth the origin is spared)
  * offload (by request and by bytes)
  * the HIT/MISS/EXPIRED/STALE/BYPASS distribution

with run-to-run sigma. Reads the per-run before/after /metrics snapshots
written by run-bench-baseline.sh and computes deltas via analyze_lib.

Usage: python3 scripts/analyze-baseline.py perf/results/baseline
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from statistics import median, pstdev

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from analyze_lib import (  # noqa: E402
    CACHE_STATUSES,
    edge_bytes_delta,
    edge_cache_delta,
    edge_entries_after,
    fmt_pct,
    origin_requests_delta,
    run_dirs,
    safe_ratio,
)


def summarize_run(run_dir: Path) -> dict:
    status = edge_cache_delta(run_dir)
    total = sum(status.values())
    byts = edge_bytes_delta(run_dir)
    total_bytes = byts["edge"] + byts["origin"]
    origin_reqs = origin_requests_delta(run_dir)
    return {
        "run": run_dir.name,
        "status": status,
        "total_responses": total,
        "hit_ratio_request": safe_ratio(status["HIT"], total),
        "hit_ratio_bytes": safe_ratio(byts["edge"], total_bytes),
        "offload_request": safe_ratio(total - origin_reqs, total) if total else float("nan"),
        "offload_bytes": safe_ratio(byts["edge"], total_bytes),
        "origin_requests": origin_reqs,
        "cache_entries": edge_entries_after(run_dir),
    }


def agg(rows: list[dict], key: str) -> tuple[float, float]:
    vals = [r[key] for r in rows if r[key] == r[key]]  # drop NaN
    if not vals:
        return float("nan"), float("nan")
    return median(vals), (pstdev(vals) if len(vals) > 1 else 0.0)


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    runs = run_dirs(base)
    if not runs:
        print(f"ERROR: no run* subdirs (with metrics snapshots) under {base}", file=sys.stderr)
        return 3
    rows = [summarize_run(r) for r in runs]

    lines = [f"# Baseline edge analysis - {base.name}", f"_{len(rows)} run(s)_", ""]
    lines.append("| run | hit% (req) | hit% (bytes) | offload% (req) | origin reqs | entries |")
    lines.append("|-----|-----------:|-------------:|---------------:|------------:|--------:|")
    for r in rows:
        lines.append(
            f"| {r['run']} | {fmt_pct(r['hit_ratio_request'])} | {fmt_pct(r['hit_ratio_bytes'])} | "
            f"{fmt_pct(r['offload_request'])} | {int(r['origin_requests'])} | {int(r['cache_entries'])} |"
        )
    lines.append("")
    lines.append("## Aggregated (median, sigma)")
    for key, label in [
        ("hit_ratio_request", "hit ratio by request"),
        ("hit_ratio_bytes", "hit ratio by bytes"),
        ("offload_request", "offload by request"),
        ("offload_bytes", "offload by bytes"),
    ]:
        m, s = agg(rows, key)
        lines.append(f"- **{label}**: median={fmt_pct(m)} sigma={fmt_pct(s)}")
    lines.append("")
    lines.append("## Cache-status distribution (summed across runs)")
    tot = {s: sum(r["status"][s] for r in rows) for s in CACHE_STATUSES}
    grand = sum(tot.values()) or 1
    for s in CACHE_STATUSES:
        lines.append(f"- {s}: {int(tot[s])} ({fmt_pct(tot[s]/grand)})")
    lines.append("")
    lines.append(
        "> Note the gap between hit-ratio-by-request and hit-ratio-by-bytes: "
        "popular objects are small (cheap hits) while the rare long-tail objects "
        "are large (expensive misses), so the origin can still serve most BYTES "
        "even at a high by-request hit ratio. Origin cost is driven by bytes."
    )

    report = "\n".join(lines) + "\n"
    (base / "report.md").write_text(report)
    (base / "report.json").write_text(json.dumps({
        "label": base.name,
        "runs": rows,
        "aggregate": {
            k: agg(rows, k) for k in
            ("hit_ratio_request", "hit_ratio_bytes", "offload_request", "offload_bytes")
        },
    }, indent=2, default=lambda o: None))
    print(report)
    print(f"Wrote {base/'report.md'} and {base/'report.json'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
