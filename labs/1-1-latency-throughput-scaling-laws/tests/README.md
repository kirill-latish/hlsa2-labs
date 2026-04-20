# Tests

Automated test suite for the latency/throughput/scaling simulator.

## Running Tests

From the lab directory (`labs/1-1-latency-throughput-scaling-laws/`):

```bash
pytest tests/ -v
```

## What Each Test File Covers

| File                   | Scope       | What it verifies |
|------------------------|-------------|------------------|
| `test_metrics.py`      | Unit        | MetricsCollector: mean, p95, thread safety, reset, summary keys |
| `test_worker_pool.py`  | Unit        | WorkerPool: serial timing, parallel speedup, jitter, no dropped requests |
| `test_workload.py`     | Unit        | Workload generators: correct counts, unique IDs, multiplier |
| `test_runner.py`       | Smoke       | Runner loads config, produces 3 conditions with correct fields |
| `test_benchmark_e2e.py`| Integration | Full benchmark output, CLI entry point, p95 > mean under saturation |

## Test Configuration

Tests use small parameters (5ms service time, 2 workers, 20 requests)
via the `tmp_config` fixture in `conftest.py`. The full suite runs in
under 2 seconds.

## Adding Your Own Tests

After implementing your improvement, add tests to verify its behaviour.
Examples:

**Bounded queue (Option B):**
```python
def test_bounded_queue_rejects_under_load(tmp_config):
    """With max_queue_depth=10, saturated workload should reject requests."""
    results = run_benchmark(tmp_config)
    saturated = [r for r in results if r["condition"] == "Saturated"][0]
    assert saturated["rejected"] > 0
```

**Request timeout (Option C):**
```python
def test_timeout_cancels_slow_requests(tmp_config):
    """Requests exceeding the deadline should be cancelled, not served late."""
    results = run_benchmark(tmp_config)
    saturated = [r for r in results if r["condition"] == "Saturated"][0]
    assert saturated["p95_ms"] < some_deadline_ms
```

Place your test files in this `tests/` directory and run `pytest tests/ -v`.
