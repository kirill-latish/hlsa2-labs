#!/usr/bin/env python3
"""analyze-healthcheck.py - report what a dependency hiccup did under the
configured health-check depth.

Reads perf/results/<LABEL>/{meta.json, edge-metrics-start.txt,
edge-metrics-end.txt, summary.json}. The headline signal is
min_healthy_during: 0 means the deep check cascaded (full outage);
4 means the shallow check rode the blip out.

Usage:
    python3 scripts/analyze-healthcheck.py perf/results/healthcheck-deep
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from analyze_baseline_lib import fivexx_counts  # noqa: E402


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    meta = json.loads((base / "meta.json").read_text())
    fx_end = fivexx_counts(base / "edge-metrics-end.txt")
    fx_start = fivexx_counts(base / "edge-metrics-start.txt")
    d503 = int(fx_end.get("503", 0) - fx_start.get("503", 0))
    d502 = int(fx_end.get("502", 0) - fx_start.get("502", 0))
    summary = json.loads((base / "summary.json").read_text()) if (base / "summary.json").exists() else {}
    min_healthy = meta.get("min_healthy_during")

    cascaded = isinstance(min_healthy, int) and min_healthy == 0
    lines = [f"# Health-check analysis - {base.name}\n"]
    lines.append(f"- health depth: **{meta.get('health_depth')}**")
    lines.append(f"- hiccup duration: {meta.get('hiccup_s')} s")
    lines.append(f"- **min healthy backends during hiccup: {min_healthy}**")
    lines.append(f"- 503 (no healthy backends) during run: **{d503}**")
    if d502:
        lines.append(f"- 502 during run: {d502}")
    lines.append(f"- loadgen offered={summary.get('offered')} served={summary.get('served')} failed={summary.get('failed')}")
    if cascaded:
        lines.append(
            "\n**Cascade confirmed.** A deep health check queries the shared "
            "Postgres dependency, so a brief blip failed the check on ALL four "
            "backends simultaneously. The balancer was left with zero healthy "
            "backends and returned 503 - a full outage from a few-second "
            "dependency hiccup the backends would otherwise have ridden out."
        )
    else:
        lines.append(
            "\n**Rode it out.** A shallow check only verifies the process is up, "
            "so the dependency blip did not eject any backend. Some in-flight "
            "requests errored during the blip, but the service stayed up. The "
            "trade-off: a shallow check misses a process that is up but genuinely "
            "broken downstream."
        )

    out = base / "report.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
