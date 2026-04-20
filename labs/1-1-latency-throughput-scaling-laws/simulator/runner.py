"""Benchmark runner -- loads config, runs all three workload conditions,
and prints a formatted report to stdout."""

from __future__ import annotations

import os
import time
from pathlib import Path

import yaml

from simulator.metrics import MetricsCollector
from simulator.worker_pool import WorkerPool
from simulator.workload import generate_parallel, generate_saturated, generate_serial

_SEPARATOR = "=" * 60


def load_config(config_path: str | Path | None = None) -> dict:
    if config_path is None:
        config_path = Path(__file__).resolve().parent.parent / "config.yaml"
    config_path = Path(config_path)
    with open(config_path) as f:
        return yaml.safe_load(f)


def _run_condition(
    label: str,
    workers: int,
    request_ids: list[int],
    service_time_ms: float,
    inter_arrival_ms: float | None = None,
) -> dict:
    pool = WorkerPool(workers=workers, service_time_ms=service_time_ms)
    collector = MetricsCollector()

    start = time.monotonic()
    if inter_arrival_ms is not None:
        pool.run_with_arrival_rate(request_ids, collector, inter_arrival_ms)
    else:
        pool.run(request_ids, collector)
    duration_s = time.monotonic() - start

    summary = collector.summary(duration_s)
    summary["condition"] = label
    summary["workers"] = workers
    summary["duration_s"] = round(duration_s, 3)
    return summary


def _print_report(results: list[dict]) -> None:
    print(_SEPARATOR)
    print("BENCHMARK RESULTS")
    print(_SEPARATOR)
    print()

    header = (
        f"{'Condition':<12} {'Workers':>7} {'Requests':>8} {'Rejected':>8} "
        f"{'Mean(ms)':>9} {'p95(ms)':>9} {'Throughput':>12} {'Duration':>10}"
    )
    print(header)
    print("-" * len(header))

    for r in results:
        print(
            f"{r['condition']:<12} {r['workers']:>7} {r['count']:>8} "
            f"{r['rejected']:>8} {r['mean_ms']:>9.2f} {r['p95_ms']:>9.2f} "
            f"{r['throughput_rps']:>10.2f} r/s {r['duration_s']:>8.3f} s"
        )

    print()
    print(_SEPARATOR)


def run_benchmark(config_path: str | Path | None = None) -> list[dict]:
    """Run the full benchmark suite and return results."""
    cfg = load_config(config_path)

    service_time_ms: float = cfg.get("service_time_ms", 10)
    workers: int = cfg.get("workers", os.cpu_count() or 4)
    total_requests: int = cfg.get("total_requests", 500)
    saturated_multiplier: int = cfg.get("saturated_multiplier", 3)

    print(f"Config: service_time={service_time_ms}ms, workers={workers}, "
          f"requests={total_requests}, saturated_multiplier={saturated_multiplier}")
    print()

    results: list[dict] = []

    # --- Serial (1 worker) ---
    serial_ids = generate_serial(total_requests)
    results.append(
        _run_condition("Serial", 1, serial_ids, service_time_ms)
    )

    # --- Parallel (N workers) ---
    parallel_ids = generate_parallel(total_requests)
    results.append(
        _run_condition("Parallel", workers, parallel_ids, service_time_ms)
    )

    # --- Saturated (N workers, excess load with arrival rate) ---
    saturated_ids = generate_saturated(total_requests, saturated_multiplier)
    capacity_rps = workers / (service_time_ms / 1000.0)
    overload_rps = capacity_rps * 1.5
    inter_arrival_ms = 1000.0 / overload_rps
    results.append(
        _run_condition(
            "Saturated", workers, saturated_ids, service_time_ms,
            inter_arrival_ms=inter_arrival_ms,
        )
    )

    _print_report(results)
    return results
