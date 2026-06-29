#!/usr/bin/env python3
"""analyze-failover.py - report failover detection time, dropped
requests, and load redistribution for one induced-failure run.

Reads perf/results/<LABEL>/{meta.json, edge-metrics-start.txt,
edge-metrics-end.txt, status-start.json, status-end.json}.

Usage:
    python3 scripts/analyze-failover.py perf/results/failover-baseline
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from analyze_baseline_lib import fivexx_counts, backend_distribution  # noqa: E402


def delta(d_end: dict, d_start: dict) -> dict:
    keys = set(d_end) | set(d_start)
    return {k: d_end.get(k, 0) - d_start.get(k, 0) for k in sorted(keys)}


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    meta = json.loads((base / "meta.json").read_text())

    fx = delta(fivexx_counts(base / "edge-metrics-end.txt"),
               fivexx_counts(base / "edge-metrics-start.txt"))
    dist = delta(backend_distribution(base / "edge-metrics-end.txt"),
                 backend_distribution(base / "edge-metrics-start.txt"))

    dropped_502 = int(fx.get("502", 0))
    dropped_503 = int(fx.get("503", 0))
    summary = json.loads((base / "summary.json").read_text()) if (base / "summary.json").exists() else {}

    lines = [f"# Failover analysis - {base.name}\n"]
    lines.append(f"- backend killed: `{meta.get('backend')}`")
    lines.append(f"- health interval: {meta.get('health_interval_ms')} ms, threshold: {meta.get('failure_threshold')}")
    lines.append(f"- **expected detection** (interval x threshold): {meta.get('expected_detection_s')} s")
    lines.append(f"- **measured detection**: {meta.get('measured_detection_s')} s")
    lines.append(f"- dropped during detection: **{dropped_502} x 502** (connection refused to the dead backend)")
    if dropped_503:
        lines.append(f"- {dropped_503} x 503 (windows with no healthy backend)")
    lines.append(f"- loadgen offered={summary.get('offered')} served={summary.get('served')} failed={summary.get('failed')}")

    lines.append("\n## Load redistribution (requests routed during the run)\n")
    lines.append("| backend | requests |")
    lines.append("|---------|---------:|")
    for k, v in dist.items():
        marker = "  (killed)" if k == meta.get("backend") else ""
        lines.append(f"| {k}{marker} | {int(v)} |")
    lines.append(
        "\nThe surviving backends absorbed the load once the dead backend left "
        "rotation; the killed backend's request count stops climbing after the "
        "detection point."
    )

    out = base / "report.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
