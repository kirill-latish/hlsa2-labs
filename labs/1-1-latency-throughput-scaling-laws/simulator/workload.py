"""Workload generators for serial, parallel, and saturated conditions."""

from __future__ import annotations


def generate_serial(total_requests: int) -> list[int]:
    """Return request IDs for a serial (single-worker) workload."""
    return list(range(1, total_requests + 1))


def generate_parallel(total_requests: int) -> list[int]:
    """Return request IDs for a parallel (multi-worker) workload.

    Same IDs as serial -- the difference is in how the runner
    configures the worker pool (N workers instead of 1).
    """
    return list(range(1, total_requests + 1))


def generate_saturated(total_requests: int, multiplier: int = 3) -> list[int]:
    """Return request IDs for a saturated workload.

    Generates ``total_requests * multiplier`` requests so that the
    offered load significantly exceeds the pool's capacity, forcing
    queue buildup and demonstrating the saturation knee.
    """
    total = total_requests * multiplier
    return list(range(1, total + 1))
