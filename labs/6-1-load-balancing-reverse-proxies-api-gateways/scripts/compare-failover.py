#!/usr/bin/env python3
"""compare-failover.py - BEFORE vs AFTER failover tuning: detection time
and dropped-request count side by side.

Usage:
    python3 scripts/compare-failover.py perf/results/failover-baseline perf/results/failover-after
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from analyze_baseline_lib import fivexx_counts  # noqa: E402


def metrics_for(base: Path) -> dict:
    meta = json.loads((base / "meta.json").read_text())
    fx_end = fivexx_counts(base / "edge-metrics-end.txt")
    fx_start = fivexx_counts(base / "edge-metrics-start.txt")
    dropped = int(fx_end.get("502", 0) - fx_start.get("502", 0))
    return {
        "name": base.name,
        "measured_detection_s": meta.get("measured_detection_s"),
        "expected_detection_s": meta.get("expected_detection_s"),
        "interval_ms": meta.get("health_interval_ms"),
        "threshold": meta.get("failure_threshold"),
        "dropped_502": dropped,
    }


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    before = metrics_for(Path(sys.argv[1]))
    after = metrics_for(Path(sys.argv[2]))

    lines = [f"# Failover compare: {before['name']} vs {after['name']}\n"]
    lines.append("| metric | before | after |")
    lines.append("|--------|-------:|------:|")
    lines.append(f"| health interval (ms) | {before['interval_ms']} | {after['interval_ms']} |")
    lines.append(f"| failure threshold | {before['threshold']} | {after['threshold']} |")
    lines.append(f"| expected detection (s) | {before['expected_detection_s']} | {after['expected_detection_s']} |")
    lines.append(f"| measured detection (s) | {before['measured_detection_s']} | {after['measured_detection_s']} |")
    lines.append(f"| dropped (502) | {before['dropped_502']} | {after['dropped_502']} |")
    lines.append(
        "\n**Residual trade-off:** faster health checks cut detection time but "
        "increase health-check load on the backends and raise the risk of "
        "flapping on a marginal backend that is briefly slow."
    )

    out = Path(sys.argv[2]) / "compare-vs-before.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
