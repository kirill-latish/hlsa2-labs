"""Shared fixtures for the simulator test suite.

All fixtures use small request counts and short service times so that
the full suite completes in under 2 seconds.
"""

from __future__ import annotations

import textwrap
from pathlib import Path

import pytest

from simulator.metrics import MetricsCollector


@pytest.fixture()
def tmp_config(tmp_path: Path) -> Path:
    """Write a small config.yaml for fast test runs and return its path."""
    config = tmp_path / "config.yaml"
    config.write_text(textwrap.dedent("""\
        service_time_ms: 5
        workers: 2
        total_requests: 20
        saturated_multiplier: 2
    """))
    return config


@pytest.fixture()
def small_collector() -> MetricsCollector:
    """Return a MetricsCollector pre-loaded with known latency values."""
    c = MetricsCollector()
    for v in [10.0, 12.0, 11.0, 15.0, 9.0, 13.0, 14.0, 10.5, 11.5, 12.5]:
        c.record(v)
    return c
