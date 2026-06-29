#!/usr/bin/env python3
"""analyze-stampede.py - compute the fan-in ratio at TTL expiration for
a stampede run.

Definition used by this lab (documented in the README): during the run
the single hot key expires once per TTL window, so the number of
*logical* refreshes is duration / TTL. Without coalescing, each expiry
triggers hundreds of concurrent SoR fetches; with singleflight/xfetch/
swr it collapses to ~1. So:

    fan_in_ratio = total_source_fetches / expected_expiries

A healthy (coalesced) run lands near 1.0; an unmitigated stampede lands
in the hundreds. We also report source_fetches / misses for reference.

Writes <label-dir>/fan_in_ratio.json and <label-dir>/report.md.

Usage:
    python3 scripts/analyze-stampede.py perf/results/stampede-baseline
"""

from __future__ import annotations

import json
import math
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from prom_lib import load, delta_counter, read_percentiles  # noqa: E402


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    run = base / "run1"
    if not run.exists():
        print(f"ERROR: {run} not found - run make inject-stampede first", file=sys.stderr)
        return 3

    before = load(run / "app-metrics-before.txt")
    after = load(run / "app-metrics.txt")
    meta = json.loads((run / "meta.json").read_text()) if (run / "meta.json").exists() else {}
    ttl = max(float(meta.get("ttl_s", 30)), 1.0)
    dur = max(float(meta.get("duration_s", 180)), 1.0)
    coalescing = (meta.get("config") or {}).get("coalescing", "?")
    jitter = (meta.get("config") or {}).get("ttl_jitter_pct", 0)

    source_fetches = delta_counter(before, after, "lab51_cache_source_fetches_total")
    misses = delta_counter(before, after, "lab51_cache_misses_total")
    expected_expiries = max(1.0, math.floor(dur / ttl))

    fan_in_ratio = source_fetches / expected_expiries
    fan_in_per_miss = (source_fetches / misses) if misses > 0 else math.nan

    pct = read_percentiles(after, "lab51_cache_read_duration_seconds_bucket", [0.50, 0.99])

    result = {
        "label": meta.get("label", base.name),
        "coalescing": coalescing,
        "ttl_jitter_pct": jitter,
        "ttl_s": ttl,
        "duration_s": dur,
        "expected_expiries": expected_expiries,
        "source_fetches": source_fetches,
        "cache_misses": misses,
        "fan_in_ratio": fan_in_ratio,
        "fan_in_per_miss": fan_in_per_miss,
        "read_p50_ms": pct[0.50],
        "read_p99_ms": pct[0.99],
    }
    (base / "fan_in_ratio.json").write_text(json.dumps(result, indent=2) + "\n")

    lines = [
        f"# Stampede analysis - {base.name}\n",
        f"- coalescing: **{coalescing}**, ttl_jitter_pct: {jitter}",
        f"- TTL={ttl:.0f}s over {dur:.0f}s => expected expiries: {expected_expiries:.0f}",
        f"- source_fetches: {source_fetches:.0f}, cache_misses: {misses:.0f}",
        f"- **fan_in_ratio (fetches per expiry): {fan_in_ratio:.1f}**",
        f"- fan_in_per_miss: {fan_in_per_miss:.3f}",
        f"- read p50: {pct[0.50]:.1f} ms, read p99: {pct[0.99]:.1f} ms",
    ]
    (base / "report.md").write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {base / 'fan_in_ratio.json'} and {base / 'report.md'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
