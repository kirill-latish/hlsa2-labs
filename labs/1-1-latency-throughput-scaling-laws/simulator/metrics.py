"""Thread-safe metrics collector for latency and throughput measurement."""

from __future__ import annotations

import statistics
import threading


class MetricsCollector:
    """Collects per-request latencies in a thread-safe manner and computes
    summary statistics (mean, p95, throughput)."""

    def __init__(self) -> None:
        self._latencies: list[float] = []
        self._lock = threading.Lock()
        self._rejected = 0

    def record(self, latency_ms: float) -> None:
        with self._lock:
            self._latencies.append(latency_ms)

    def record_rejection(self) -> None:
        with self._lock:
            self._rejected += 1

    @property
    def count(self) -> int:
        with self._lock:
            return len(self._latencies)

    @property
    def rejected(self) -> int:
        with self._lock:
            return self._rejected

    def mean(self) -> float:
        with self._lock:
            if not self._latencies:
                return 0.0
            return statistics.mean(self._latencies)

    def p95(self) -> float:
        with self._lock:
            if not self._latencies:
                return 0.0
            sorted_lat = sorted(self._latencies)
            idx = int(len(sorted_lat) * 0.95)
            idx = min(idx, len(sorted_lat) - 1)
            return sorted_lat[idx]

    def throughput(self, duration_s: float) -> float:
        with self._lock:
            if duration_s <= 0:
                return 0.0
            return len(self._latencies) / duration_s

    def summary(self, duration_s: float) -> dict:
        with self._lock:
            if not self._latencies:
                return {
                    "mean_ms": 0.0,
                    "p95_ms": 0.0,
                    "count": 0,
                    "rejected": self._rejected,
                    "throughput_rps": 0.0,
                }
            sorted_lat = sorted(self._latencies)
            idx = min(int(len(sorted_lat) * 0.95), len(sorted_lat) - 1)
            return {
                "mean_ms": round(statistics.mean(self._latencies), 2),
                "p95_ms": round(sorted_lat[idx], 2),
                "count": len(self._latencies),
                "rejected": self._rejected,
                "throughput_rps": round(len(self._latencies) / max(duration_s, 1e-9), 2),
            }

    def reset(self) -> None:
        with self._lock:
            self._latencies.clear()
            self._rejected = 0
