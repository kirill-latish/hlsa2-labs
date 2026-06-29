#!/usr/bin/env python3
"""analyze-5xx.py - attribute each edge 5xx class to its scenario and
confirm you can read the code as a layer signal.

Reads perf/results/<LABEL>/edge-metrics-{start,502,503,504}.txt and
computes the per-scenario delta of lab61_edge_5xx_total.

Usage:
    python3 scripts/analyze-5xx.py perf/results/5xx
"""

from __future__ import annotations

import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from analyze_baseline_lib import fivexx_counts  # noqa: E402

LAYER = {
    "502": "connectivity (proxy could not reach the backend)",
    "503": "capacity/health (no healthy backends)",
    "504": "backend latency (backend reached but too slow)",
}


def delta(end: dict, start: dict, code: str) -> int:
    return int(end.get(code, 0) - start.get(code, 0))


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    snaps = {
        "start": fivexx_counts(base / "edge-metrics-start.txt"),
        "502": fivexx_counts(base / "edge-metrics-502.txt"),
        "503": fivexx_counts(base / "edge-metrics-503.txt"),
        "504": fivexx_counts(base / "edge-metrics-504.txt"),
    }
    # Each scenario's contribution = delta vs the previous snapshot.
    contrib = {
        "502": delta(snaps["502"], snaps["start"], "502"),
        "503": delta(snaps["503"], snaps["502"], "503"),
        "504": delta(snaps["504"], snaps["503"], "504"),
    }

    lines = [f"# 5xx classification - {base.name}\n"]
    lines.append("| scenario | code | count | layer signal |")
    lines.append("|----------|------|------:|--------------|")
    all_present = True
    for code in ("502", "503", "504"):
        c = contrib[code]
        if c <= 0:
            all_present = False
        lines.append(f"| {code} scenario | {code} | {c} | {LAYER[code]} |")
    lines.append(
        "\nIn a real incident the difference between 502, 503, and 504 tells you "
        "which layer to investigate first: 502 -> connectivity, 503 -> "
        "capacity/health, 504 -> backend latency."
    )
    if not all_present:
        lines.append(
            "\n> NOTE: at least one class did not register. Re-run with a longer "
            "SCENARIO_S, or check that the previous scenario was fully restored "
            "before the next one started."
        )

    out = base / "report.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print(f"\nWrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
