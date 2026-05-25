#!/usr/bin/env python3
"""analyze-blast-radius.py - compute the fraction of the critical
journey that broke once a fault was injected.

Reads runN/gateway-metrics.txt under <LABEL>/. The "blast radius" is
defined as 1 - success_ratio of /checkout from the gateway's point of
view, plus a per-dep breakdown showing which deps broke (success_ratio
< 99%).

Usage:
    python3 scripts/analyze-blast-radius.py perf/results/faulted-before
"""

from __future__ import annotations

import math
import re
import sys
from collections import defaultdict
from pathlib import Path

METRIC_RE = re.compile(r'^([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\}\s+([0-9eE.+\-]+)\s*$')


def parse_prom(text: str) -> dict[str, list[tuple[dict[str, str], float]]]:
    out: dict[str, list[tuple[dict[str, str], float]]] = {}
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        m = METRIC_RE.match(line)
        if not m:
            continue
        name, labels_str, value = m.groups()
        labels = {}
        for kv in re.findall(r'([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"', labels_str):
            labels[kv[0]] = kv[1]
        try:
            out.setdefault(name, []).append((labels, float(value)))
        except ValueError:
            pass
    return out


def summarize_run(run_dir: Path) -> dict:
    metrics = run_dir / "gateway-metrics.txt"
    if not metrics.exists():
        return {}
    parsed = parse_prom(metrics.read_text())

    journey: dict[str, float] = defaultdict(float)
    for labels, val in parsed.get("lab33_gateway_checkout_total", []):
        journey[labels.get("outcome", "?")] += val
    total = sum(journey.values())
    success = journey["success_full"] + journey["success_degraded"]
    blast = 1.0 - (success / total) if total > 0 else math.nan

    dep_calls: dict[tuple[str, str, str], float] = defaultdict(float)
    for labels, val in parsed.get("lab33_gateway_dep_calls_total", []):
        key = (labels.get("dep", "?"), labels.get("critical", "?"), labels.get("outcome", "?"))
        dep_calls[key] += val
    per_dep: dict[str, dict[str, float]] = defaultdict(lambda: {"total": 0.0, "success": 0.0, "critical": "false"})
    for (dep, crit, outcome), v in dep_calls.items():
        per_dep[dep]["total"] += v
        per_dep[dep]["critical"] = crit
        if outcome == "success":
            per_dep[dep]["success"] += v

    fallbacks: dict[str, float] = defaultdict(float)
    for labels, val in parsed.get("lab33_gateway_fallbacks_served_total", []):
        fallbacks[labels.get("dep", "?")] += val

    return {
        "run": run_dir.name,
        "journey": dict(journey),
        "total": total,
        "blast_radius": blast,
        "per_dep": {k: {"success_ratio": (v["success"] / v["total"]) if v["total"] > 0 else math.nan,
                        "critical": v["critical"] == "true",
                        "calls": int(v["total"])} for k, v in per_dep.items()},
        "fallbacks": {k: int(v) for k, v in fallbacks.items()},
    }


def run_dirs(base: Path) -> list[Path]:
    if not base.exists():
        return []
    out = sorted(d for d in base.iterdir() if d.is_dir() and d.name.startswith("run"))
    if not out and (base / "gateway-metrics.txt").exists():
        return [base]
    return out


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    runs = run_dirs(base)
    if not runs:
        print(f"ERROR: no runs under {base}", file=sys.stderr)
        return 3

    rows = [summarize_run(r) for r in runs]
    blasts = [r["blast_radius"] for r in rows if not (isinstance(r["blast_radius"], float) and math.isnan(r["blast_radius"]))]
    median_blast = sum(blasts) / len(blasts) if blasts else math.nan

    lines: list[str] = []
    lines.append(f"# Blast-radius analysis - {base.name}\n")
    lines.append(f"\n_{len(rows)} runs; mean blast radius (1 - success_ratio): {median_blast:.4f}_\n")
    lines.append("\n## Critical journey outcomes\n")
    lines.append("| run | success_full | success_degraded | failed | shed | total | blast_radius |")
    lines.append("|-----|--------------|------------------|--------|------|-------|--------------|")
    for r in rows:
        j = r["journey"]
        lines.append(
            f"| {r['run']} | {int(j.get('success_full', 0))} | {int(j.get('success_degraded', 0))} | "
            f"{int(j.get('failed', 0))} | {int(j.get('shed', 0))} | {int(r['total'])} | "
            f"{r['blast_radius']*100:.2f}% |"
        )

    lines.append("\n## Per-dependency success ratio (last run)\n")
    if rows:
        per_dep = rows[-1]["per_dep"]
        fbs = rows[-1]["fallbacks"]
        lines.append("| dep | critical | calls | success_ratio | fallbacks_served |")
        lines.append("|-----|----------|-------|---------------|------------------|")
        for dep, info in sorted(per_dep.items()):
            sr = info["success_ratio"]
            sr_str = f"{sr*100:.2f}%" if not math.isnan(sr) else "n/a"
            lines.append(f"| {dep} | {info['critical']} | {info['calls']} | {sr_str} | {fbs.get(dep, 0)} |")

    out = base / "report.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print()
    print(f"Wrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
