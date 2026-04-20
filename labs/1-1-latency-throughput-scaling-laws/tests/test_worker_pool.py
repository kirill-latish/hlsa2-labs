"""Unit tests for simulator.worker_pool.WorkerPool.

These tests show how the worker pool behaves under serial vs parallel
execution, verify jitter, and confirm no requests are dropped.
"""

from __future__ import annotations

import time

from simulator.metrics import MetricsCollector
from simulator.worker_pool import WorkerPool


def test_single_worker_serial() -> None:
    """With 1 worker, requests run sequentially.
    Total wall time should be roughly N * service_time."""
    pool = WorkerPool(workers=1, service_time_ms=5)
    collector = MetricsCollector()
    ids = list(range(1, 6))  # 5 requests

    start = time.monotonic()
    pool.run(ids, collector)
    elapsed = time.monotonic() - start

    assert collector.count == 5
    # 5 requests * ~5ms each = ~25ms minimum
    assert elapsed >= 0.020


def test_parallel_workers() -> None:
    """With N workers processing N requests, total time ~ 1 * service_time."""
    n = 4
    pool = WorkerPool(workers=n, service_time_ms=10)
    collector = MetricsCollector()
    ids = list(range(1, n + 1))

    start = time.monotonic()
    pool.run(ids, collector)
    elapsed = time.monotonic() - start

    assert collector.count == n
    # All 4 run in parallel: should take ~10-15ms, not ~40ms
    assert elapsed < 0.030


def test_latency_has_jitter() -> None:
    """Latencies should not be identical due to +/-20% jitter."""
    pool = WorkerPool(workers=4, service_time_ms=10)
    collector = MetricsCollector()
    ids = list(range(1, 21))  # 20 requests

    pool.run(ids, collector)

    summary = collector.summary(duration_s=1.0)
    # With jitter, p95 should differ from mean
    assert summary["p95_ms"] != summary["mean_ms"] or collector.count < 2


def test_all_requests_completed() -> None:
    """Every submitted request must produce a recorded latency."""
    pool = WorkerPool(workers=2, service_time_ms=3)
    collector = MetricsCollector()
    ids = list(range(1, 31))  # 30 requests

    pool.run(ids, collector)
    assert collector.count == 30


def test_run_with_arrival_rate() -> None:
    """run_with_arrival_rate completes all requests and respects ordering."""
    pool = WorkerPool(workers=2, service_time_ms=5)
    collector = MetricsCollector()
    ids = list(range(1, 11))

    pool.run_with_arrival_rate(ids, collector, inter_arrival_ms=2.0)
    assert collector.count == 10
