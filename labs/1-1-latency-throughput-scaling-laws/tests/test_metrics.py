"""Unit tests for simulator.metrics.MetricsCollector.

These tests demonstrate how the MetricsCollector works and verify
its correctness.  Students can use them as a reference when adding
metrics for their own improvements (e.g. rejection counters).
"""

from __future__ import annotations

import threading

from simulator.metrics import MetricsCollector


def test_mean_latency(small_collector: MetricsCollector) -> None:
    """Mean of the pre-loaded values should match manual calculation."""
    expected = (10.0 + 12.0 + 11.0 + 15.0 + 9.0 + 13.0 + 14.0 + 10.5 + 11.5 + 12.5) / 10
    assert abs(small_collector.mean() - expected) < 0.01


def test_p95_latency(small_collector: MetricsCollector) -> None:
    """p95 of 10 values: the 95th percentile index is int(10*0.95)=9,
    so p95 should be the 10th-largest value (15.0)."""
    assert small_collector.p95() == 15.0


def test_p95_with_100_values() -> None:
    """With 100 sequential values 1..100, p95 should be ~95."""
    c = MetricsCollector()
    for i in range(1, 101):
        c.record(float(i))
    assert 94.0 <= c.p95() <= 96.0


def test_empty_collector_returns_zero() -> None:
    """An empty collector returns 0.0 for all stats instead of crashing."""
    c = MetricsCollector()
    assert c.mean() == 0.0
    assert c.p95() == 0.0
    assert c.count == 0


def test_thread_safety() -> None:
    """Concurrent recording from multiple threads must not lose data."""
    c = MetricsCollector()
    per_thread = 100
    num_threads = 10

    def record_many():
        for i in range(per_thread):
            c.record(float(i))

    threads = [threading.Thread(target=record_many) for _ in range(num_threads)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    assert c.count == per_thread * num_threads


def test_summary_keys(small_collector: MetricsCollector) -> None:
    """summary() must return a dict with the expected keys."""
    s = small_collector.summary(duration_s=1.0)
    expected_keys = {"mean_ms", "p95_ms", "count", "rejected", "throughput_rps"}
    assert set(s.keys()) == expected_keys


def test_summary_throughput() -> None:
    """Throughput = count / duration."""
    c = MetricsCollector()
    for _ in range(50):
        c.record(10.0)
    s = c.summary(duration_s=5.0)
    assert s["throughput_rps"] == 10.0


def test_record_rejection() -> None:
    """record_rejection() increments the rejected counter."""
    c = MetricsCollector()
    assert c.rejected == 0
    c.record_rejection()
    c.record_rejection()
    assert c.rejected == 2


def test_reset() -> None:
    """reset() clears all collected data."""
    c = MetricsCollector()
    c.record(10.0)
    c.record_rejection()
    c.reset()
    assert c.count == 0
    assert c.rejected == 0
