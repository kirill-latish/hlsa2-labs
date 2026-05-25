#!/usr/bin/env python3
"""analyze-overload.py - compare the "storm" and "tamed" overload runs.

Reads perf/results/overload/{storm,tamed}/summary.json (loadgen) and
gateway-metrics.txt (Prometheus). Reports:

  - offered vs served rps for each run
  - retry counts and retry-amplification (retries / served)
  - inbound shed counts (429s) and per-dep shed (503s)
  - whether load-shed and retry-budget were enabled

Usage:
    python3 scripts/analyze-overload.py perf/results/overload
"""

from __future__ import annotations

import json
import re
import sys
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


def summarize(run_dir: Path) -> dict:
    out = {"label": run_dir.name}
    sj = run_dir / "summary.json"
    if sj.exists():
        s = json.loads(sj.read_text())
        out["offered"] = int(s.get("offered", 0))
        out["served"] = int(s.get("served", 0))
        out["retries"] = int(s.get("retries", 0))
        out["failed"] = int(s.get("failed", 0))
        out["duration_s"] = int(s.get("duration_s", 0))
        out["rate_rps"] = int(s.get("rate_rps", 0))
    meta = run_dir / "meta.json"
    if meta.exists():
        m = json.loads(meta.read_text())
        out["controls"] = m.get("controls", {})
    gm = run_dir / "gateway-metrics.txt"
    if gm.exists():
        parsed = parse_prom(gm.read_text())
        shed = 0.0
        per_dep_shed: dict[str, float] = {}
        for labels, v in parsed.get("lab33_gateway_shed_total", []):
            shed += v
            if labels.get("scope") == "dep_503":
                per_dep_shed[labels.get("dep", "?")] = per_dep_shed.get(labels.get("dep", "?"), 0) + v
        out["shed_total"] = int(shed)
        out["shed_by_dep"] = {k: int(v) for k, v in per_dep_shed.items()}
        ret = 0.0
        for _, v in parsed.get("lab33_gateway_retries_total", []):
            ret += v
        out["gateway_retries"] = int(ret)
    return out


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = Path(sys.argv[1])
    rows = []
    for name in ("storm", "tamed"):
        d = base / name
        if d.exists():
            rows.append(summarize(d))
        else:
            print(f"WARN: {d} not found (run `make bench-overload LABEL={name} ...` first)", file=sys.stderr)
    if not rows:
        return 3

    lines: list[str] = []
    lines.append(f"# Overload analysis - {base}\n")
    lines.append("\n| label | controls | offered | served | retries | failed | shed_total | offered_rps | served_rps | retry_amp |")
    lines.append("|-------|----------|---------|--------|---------|--------|------------|-------------|------------|-----------|")
    for r in rows:
        dur = max(r.get("duration_s", 1), 1)
        offered_rps = r.get("offered", 0) / dur
        served_rps = r.get("served", 0) / dur
        retries = r.get("retries", 0)
        amp = retries / max(r.get("served", 1), 1)
        ctrls = r.get("controls", {})
        ctrl_str = ",".join(f"{k}={v}" for k, v in ctrls.items() if v == "on") or "all-off"
        lines.append(
            f"| {r['label']} | {ctrl_str} | {r.get('offered', 0)} | {r.get('served', 0)} | "
            f"{retries} | {r.get('failed', 0)} | {r.get('shed_total', 0)} | "
            f"{offered_rps:.1f} | {served_rps:.1f} | {amp:.2f} |"
        )

    lines.append("\n## Decision\n")
    if len(rows) == 2:
        storm, tamed = rows
        storm_amp = storm.get("retries", 0) / max(storm.get("served", 1), 1)
        tamed_amp = tamed.get("retries", 0) / max(tamed.get("served", 1), 1)
        storm_succ = storm.get("served", 0) / max(storm.get("offered", 1), 1)
        tamed_succ = tamed.get("served", 0) / max(tamed.get("offered", 1), 1)
        verdict = (
            "tamed improves success ratio AND lowers retry amplification"
            if tamed_succ >= storm_succ and tamed_amp < storm_amp
            else "WARN: tamed did not strictly dominate storm - inspect run logs"
        )
        lines.append(
            f"- storm: success={storm_succ*100:.2f}% retry_amp={storm_amp:.2f}\n"
            f"- tamed: success={tamed_succ*100:.2f}% retry_amp={tamed_amp:.2f}\n"
            f"- verdict: **{verdict}**"
        )

    out = base / "report.md"
    out.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    print()
    print(f"Wrote {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
