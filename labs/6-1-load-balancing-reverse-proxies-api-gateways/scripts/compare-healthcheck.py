#!/usr/bin/env python3
"""compare-healthcheck.py - deep vs shallow health check under the same
dependency hiccup, side by side.

Usage:
    python3 scripts/compare-healthcheck.py perf/results/healthcheck-deep perf/results/healthcheck-shallow
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from analyze_baseline_lib import fivexx_counts  # noqa: E402


def row(base: Path) -> dict:
    meta = json.loads((base / "meta.json").read_text())
    fx_end = fivexx_counts(base / "edge-metrics-end.txt")
    fx_start = fivexx_counts(base / "edge-metrics-start.txt")
    summary = json.loads((base / "summary.json").read_text()) if (base / "summary.json").exists() else {}
    return {
        "name": base.name,
        "depth": meta.get("health_depth"),
        "min_healthy": meta.get("min_healthy_during"),
        "d503": int(fx_end.get("503", 0) - fx_start.get("503", 0)),
        "served": summary.get("served"),
        "failed": summary.get("failed"),
    }


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    a = row(Path(sys.argv[1]))
    b = row(Path(sys.argv[2]))

    lines = ["# Health-check compare: deep vs shallow\n"]
    lines.append("| metric | " + f"{a['name']} ({a['depth']})" + " | " + f"{b['name']} ({b['depth']})" + " |")
    lines.append("|--------|---|---|")
    lines.append(f"| min healthy backends during hiccup | {a['min_healthy']} | {b['min_healthy']} |")
    lines.append(f"| 503 (no healthy backends) | {a['d503']} | {b['d503']} |")
    lines.append(f"| served | {a['served']} | {b['served']} |")
    lines.append(f"| failed | {a['failed']} | {b['failed']} |")
    lines.append(
        "\nThe deep check converts a brief shared-dependency blip into a full "
        "outage (min healthy -> 0, 503 spike); the shallow check keeps every "
        "backend in rotation and the service rides the blip out. The deepest "
        "lesson of the topic: 'check everything' is sometimes catastrophically "
        "wrong."
    )

    out = Path(sys.argv[2]) / "compare-vs-deep.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
