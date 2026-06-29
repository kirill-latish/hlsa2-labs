#!/usr/bin/env python3
"""compare-hot-key.py - per-shard Redis ops imbalance before vs after the
local-LRU fix, plus the shared-cache source-fetch rate.

Per-node ops are diffed between app-metrics-before.txt and
app-metrics.txt for each label's run1. The imbalance metric is the
hottest shard's share of total ops (1/N == perfectly balanced).

Writes <after-dir>/compare-hot-key.md and a per-shard.json in each dir.

Usage:
    python3 scripts/compare-hot-key.py perf/results/hot-baseline perf/results/hot-after
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from prom_lib import load, counter_by_label, delta_counter  # noqa: E402


def per_shard(d: Path) -> dict:
    run = d / "run1"
    before = load(run / "app-metrics-before.txt")
    after = load(run / "app-metrics.txt")
    ops_before = counter_by_label(before, "lab51_redis_ops_total", "node")
    ops_after = counter_by_label(after, "lab51_redis_ops_total", "node")
    nodes = sorted(set(ops_before) | set(ops_after))
    ops = {n: ops_after.get(n, 0.0) - ops_before.get(n, 0.0) for n in nodes}
    total = sum(ops.values())
    shares = {n: (v / total if total > 0 else 0.0) for n, v in ops.items()}
    hottest = max(shares.values()) if shares else 0.0
    source_fetches = delta_counter(before, after, "lab51_cache_source_fetches_total")

    result = {
        "label": d.name,
        "ops_by_node": ops,
        "share_by_node": shares,
        "hottest_share": hottest,
        "n_shards": len(nodes),
        "source_fetches": source_fetches,
    }
    (d / "per-shard.json").write_text(json.dumps(result, indent=2) + "\n")
    return result


def fmt_shares(r: dict) -> str:
    return ", ".join(f"{n}={r['share_by_node'][n]*100:.0f}%" for n in sorted(r["share_by_node"]))


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    before = per_shard(Path(sys.argv[1]))
    after = per_shard(Path(sys.argv[2]))
    balanced = 1.0 / max(before["n_shards"], 1)

    lines = [
        "# Hot-key per-shard comparison\n",
        f"_Perfectly balanced share would be {balanced*100:.0f}% per shard._\n",
        "| label | per-shard share | hottest share | shared source_fetches |",
        "|-------|-----------------|---------------|-----------------------|",
        f"| {before['label']} | {fmt_shares(before)} | {before['hottest_share']*100:.0f}% | {before['source_fetches']:.0f} |",
        f"| {after['label']} | {fmt_shares(after)} | {after['hottest_share']*100:.0f}% | {after['source_fetches']:.0f} |",
        "",
        f"Hottest-shard share moved {before['hottest_share']*100:.0f}% -> {after['hottest_share']*100:.0f}%; "
        f"shared source fetches {before['source_fetches']:.0f} -> {after['source_fetches']:.0f}.",
    ]
    out = Path(sys.argv[2]) / "compare-hot-key.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
