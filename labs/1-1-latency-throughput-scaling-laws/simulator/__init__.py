"""Latency / Throughput / Scaling Laws simulator.

Quick smoke-test::

    >>> import simulator
    >>> print('simulator OK')
    simulator OK
"""

from simulator.runner import run_benchmark

__all__ = ["run_benchmark"]
