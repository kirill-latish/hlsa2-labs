"""End-to-end integration tests.

These tests run the full benchmark (via subprocess or direct call)
and verify the output structure and key invariants.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

from simulator.runner import run_benchmark


def test_full_benchmark_completes(tmp_config: Path) -> None:
    """The benchmark runs to completion without errors."""
    results = run_benchmark(tmp_config)
    assert len(results) == 3
    for r in results:
        assert r["count"] > 0
        assert r["mean_ms"] > 0
        assert r["throughput_rps"] > 0


def test_output_parseable(tmp_config: Path, capsys) -> None:
    """Benchmark prints numeric latency and throughput to stdout."""
    run_benchmark(tmp_config)
    captured = capsys.readouterr().out

    assert "BENCHMARK RESULTS" in captured
    assert "Serial" in captured
    assert "Parallel" in captured
    assert "Saturated" in captured
    assert "r/s" in captured


def test_saturated_p95_exceeds_mean(tmp_config: Path) -> None:
    """Under saturation, p95 latency should exceed mean latency.

    This validates the deliberate bottleneck in the baseline code:
    the unbounded queue causes tail latency to grow under overload.
    Students fix this in their homework improvement.
    """
    results = run_benchmark(tmp_config)
    saturated = [r for r in results if r["condition"] == "Saturated"][0]
    assert saturated["p95_ms"] >= saturated["mean_ms"]


def test_cli_entry_point(tmp_config: Path) -> None:
    """``python -m simulator <config>`` exits with code 0."""
    lab_dir = Path(__file__).resolve().parent.parent
    result = subprocess.run(
        [sys.executable, "-m", "simulator", str(tmp_config)],
        capture_output=True,
        text=True,
        cwd=str(lab_dir),
        timeout=60,
    )
    assert result.returncode == 0, f"stderr: {result.stderr}"
    assert "BENCHMARK RESULTS" in result.stdout
