"""Smoke tests for simulator.runner.

These tests verify that the runner correctly loads config and produces
output containing all three workload conditions with the expected fields.
"""

from __future__ import annotations

from pathlib import Path

from simulator.runner import load_config, run_benchmark


def test_runner_loads_config(tmp_config: Path) -> None:
    """load_config reads values from the YAML file."""
    cfg = load_config(tmp_config)
    assert cfg["service_time_ms"] == 5
    assert cfg["workers"] == 2
    assert cfg["total_requests"] == 20
    assert cfg["saturated_multiplier"] == 2


def test_runner_output_structure(tmp_config: Path) -> None:
    """run_benchmark returns a list of dicts, one per condition."""
    results = run_benchmark(tmp_config)
    assert len(results) == 3

    conditions = {r["condition"] for r in results}
    assert conditions == {"Serial", "Parallel", "Saturated"}

    for r in results:
        assert "mean_ms" in r
        assert "p95_ms" in r
        assert "throughput_rps" in r
        assert "workers" in r
        assert "count" in r
        assert "rejected" in r


def test_serial_uses_one_worker(tmp_config: Path) -> None:
    """Serial condition must use exactly 1 worker."""
    results = run_benchmark(tmp_config)
    serial = [r for r in results if r["condition"] == "Serial"][0]
    assert serial["workers"] == 1


def test_parallel_uses_configured_workers(tmp_config: Path) -> None:
    """Parallel condition must use the worker count from config."""
    results = run_benchmark(tmp_config)
    parallel = [r for r in results if r["condition"] == "Parallel"][0]
    assert parallel["workers"] == 2


def test_saturated_processes_more_requests(tmp_config: Path) -> None:
    """Saturated condition processes total_requests * multiplier."""
    results = run_benchmark(tmp_config)
    saturated = [r for r in results if r["condition"] == "Saturated"][0]
    # 20 * 2 = 40
    assert saturated["count"] == 40
