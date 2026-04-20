"""Unit tests for simulator.workload generators.

These tests verify the workload generators produce the correct number
of requests with unique IDs.
"""

from __future__ import annotations

from simulator.workload import generate_parallel, generate_saturated, generate_serial


def test_serial_workload_count() -> None:
    ids = generate_serial(50)
    assert len(ids) == 50


def test_parallel_workload_count() -> None:
    ids = generate_parallel(50)
    assert len(ids) == 50


def test_saturated_workload_count() -> None:
    ids = generate_saturated(50, multiplier=3)
    assert len(ids) == 150


def test_saturated_default_multiplier() -> None:
    ids = generate_saturated(10)
    assert len(ids) == 30  # default multiplier is 3


def test_request_ids_unique_serial() -> None:
    ids = generate_serial(100)
    assert len(set(ids)) == 100


def test_request_ids_unique_saturated() -> None:
    ids = generate_saturated(100, multiplier=2)
    assert len(set(ids)) == 200


def test_ids_start_at_one() -> None:
    """IDs should start at 1 (not 0) for human-readable output."""
    ids = generate_serial(5)
    assert ids[0] == 1
    assert ids[-1] == 5
