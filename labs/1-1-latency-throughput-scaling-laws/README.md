# Lab 1-1: Latency, Throughput, and Scaling Laws

A Python-based benchmark simulator for studying latency, throughput,
and the saturation knee in a worker-pool architecture.

## Prerequisites

- Python 3.12 or newer
- pip

## Setup

```bash
cd labs/1-1-latency-throughput-scaling-laws/
python3 -m venv .venv
source .venv/bin/activate    # Windows: .venv\Scripts\activate
pip install -r requirements.txt
```

Verify the environment:

```bash
python3 -c "import simulator; print('simulator OK')"
```

## Running the Benchmark

```bash
bash scripts/run-benchmark.sh 2>&1 | tee results/baseline.txt
```

Or directly:

```bash
python3 -m simulator
```

You can pass a custom config file:

```bash
python3 -m simulator path/to/my-config.yaml
```

## Running Tests

```bash
pytest tests/ -v
```

See [tests/README.md](tests/README.md) for details on what each test
verifies and how to add your own.

## Configuration

Edit `config.yaml` to change benchmark parameters:

| Key                    | Default | Description                                     |
|------------------------|---------|-------------------------------------------------|
| `service_time_ms`      | 10      | Simulated processing time per request (ms)      |
| `workers`              | 4       | Number of concurrent workers in the thread pool |
| `total_requests`       | 500     | Requests per condition (serial / parallel)      |
| `saturated_multiplier` | 3       | Saturated workload = total_requests * multiplier|

## Workload Conditions

The benchmark runs three conditions automatically:

1. **Serial** -- 1 worker, all requests processed sequentially.
2. **Parallel** -- N workers, all requests submitted at once.
3. **Saturated** -- N workers, but the offered load exceeds capacity
   (requests arrive at 1.5x the pool's processing rate).

## Expected Output

```
Config: service_time=10ms, workers=4, requests=500, saturated_multiplier=3

============================================================
BENCHMARK RESULTS
============================================================

Condition    Workers Requests Rejected  Mean(ms)  p95(ms)   Throughput   Duration
----------------------------------------------------------------------------------
Serial             1      500        0     10.xx     12.xx     xx.xx r/s    x.xxx s
Parallel           4      500        0     10.xx     12.xx    xxx.xx r/s    x.xxx s
Saturated          4     1500        0     xx.xx     xx.xx    xxx.xx r/s    x.xxx s

============================================================
```

## Project Structure

```
simulator/
  __init__.py       Public API (import simulator)
  __main__.py       python -m simulator entry point
  metrics.py        Thread-safe latency/throughput collector
  worker_pool.py    ThreadPoolExecutor wrapper (the code you improve)
  workload.py       Request generators for each condition
  runner.py         Orchestrator: loads config, runs conditions, prints report
scripts/
  run-benchmark.sh  Shell wrapper (activates venv, calls python -m simulator)
tests/              Automated tests (see tests/README.md)
results/            Save your benchmark output here (baseline.txt, improved.txt)
config.yaml         Tunable benchmark parameters
```

## What to Do Next

See the homework task description for the full assignment: analyse the
baseline, choose one improvement (worker tuning, bounded queue, request
timeout, or batching), implement it, re-benchmark, and write `results.md`.
