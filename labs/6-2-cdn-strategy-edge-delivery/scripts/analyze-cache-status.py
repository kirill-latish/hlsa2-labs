#!/usr/bin/env python3
"""analyze-cache-status.py - report the HIT/MISS/EXPIRED/STALE/BYPASS
distribution for a labelled run. Used to expose the 'caching nothing'
silent failure: when a Set-Cookie (or no-cache / Vary: Cookie) is glued
onto static content, the BYPASS rate spikes while HIT collapses - even
though latency and uptime look perfectly normal.

Usage: python3 scripts/analyze-cache-status.py perf/results/bypass
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from analyze_lib import CACHE_STATUSES, edge_cache_delta, fmt_pct, run_dirs, safe_ratio  # noqa: E402


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    runs = run_dirs(base)
    if not runs:
        print(f"ERROR: no run* subdirs (with metrics snapshots) under {base}", file=sys.stderr)
        return 3

    tot = {s: 0.0 for s in CACHE_STATUSES}
    for r in runs:
        d = edge_cache_delta(r)
        for s in CACHE_STATUSES:
            tot[s] += d[s]
    grand = sum(tot.values()) or 1

    lines = [f"# Cache-status distribution - {base.name}", ""]
    lines.append("| status | count | share |")
    lines.append("|--------|------:|------:|")
    for s in CACHE_STATUSES:
        lines.append(f"| {s} | {int(tot[s])} | {fmt_pct(safe_ratio(tot[s], grand))} |")
    lines.append("")
    bypass_share = safe_ratio(tot["BYPASS"], grand)
    hit_share = safe_ratio(tot["HIT"], grand)
    lines.append(f"- BYPASS share: **{fmt_pct(bypass_share)}**, HIT share: **{fmt_pct(hit_share)}**")
    if bypass_share > 0.5:
        lines.append(
            "- The BYPASS spike with collapsed HIT is the signature of the silent "
            "'caching nothing' failure. Latency and uptime panels look normal; only "
            "the cache-status distribution catches it. Monitor BYPASS/MISS by route, "
            "not just latency and uptime."
        )

    report = "\n".join(lines) + "\n"
    (base / "cache-status.md").write_text(report)
    (base / "cache-status.json").write_text(json.dumps(
        {"label": base.name, "counts": tot, "bypass_share": bypass_share, "hit_share": hit_share},
        indent=2,
    ))
    print(report)
    print(f"Wrote {base/'cache-status.md'} and {base/'cache-status.json'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
