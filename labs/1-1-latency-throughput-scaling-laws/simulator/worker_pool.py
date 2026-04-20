"""Worker pool that processes simulated requests using a thread pool.

The baseline implementation has NO queue bound, NO timeout, and NO load
shedding.  This is intentional -- the saturated workload will demonstrate
unbounded queue growth.  Students improve this in their homework.
"""

from __future__ import annotations

import random
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from simulator.metrics import MetricsCollector


class WorkerPool:
    """Fixed-size thread pool that simulates request processing."""

    def __init__(self, workers: int, service_time_ms: float) -> None:
        self.workers = workers
        self.service_time_s = service_time_ms / 1000.0

    def _handle_request(self, request_id: int) -> float:
        """Simulate processing a single request.

        Returns the measured latency in milliseconds.
        Adds +/-20 % jitter to the base service time so that latency
        distributions are realistic (not all identical).
        """
        jitter = random.uniform(0.8, 1.2)
        sleep_time = self.service_time_s * jitter
        start = time.monotonic()
        time.sleep(sleep_time)
        elapsed_ms = (time.monotonic() - start) * 1000.0
        return elapsed_ms

    def run(
        self,
        request_ids: list[int],
        collector: MetricsCollector,
    ) -> None:
        """Submit all requests to the pool and record latencies.

        Requests are submitted as fast as possible (no arrival-rate
        shaping here).  Queue depth is unbounded in the baseline.
        """
        with ThreadPoolExecutor(max_workers=self.workers) as pool:
            futures = {
                pool.submit(self._handle_request, rid): rid
                for rid in request_ids
            }
            for future in as_completed(futures):
                latency_ms = future.result()
                collector.record(latency_ms)

    def run_with_arrival_rate(
        self,
        request_ids: list[int],
        collector: MetricsCollector,
        inter_arrival_ms: float,
    ) -> None:
        """Submit requests at a controlled arrival rate.

        This models a saturated workload where requests keep arriving
        faster than the pool can drain them, causing queue buildup.
        """
        with ThreadPoolExecutor(max_workers=self.workers) as pool:
            futures: dict = {}
            for rid in request_ids:
                futures[pool.submit(self._handle_request, rid)] = rid
                time.sleep(inter_arrival_ms / 1000.0)

            for future in as_completed(futures):
                latency_ms = future.result()
                collector.record(latency_ms)
