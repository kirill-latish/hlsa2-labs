# Highload Software Architecture 2 — Labs

Hands-on labs for the **Highload Software Architecture 2** course.
Each lab is a self-contained Python project you run locally,
benchmark, improve, and document.

## Labs

| #   | Directory | Topic |
|-----|-----------|-------|
| 1-1 | [labs/1-1-latency-throughput-scaling-laws](labs/1-1-latency-throughput-scaling-laws/) | Latency, Throughput, and Scaling Laws |

## Getting Started

1. **Fork** this repository on GitHub so you have your own copy.
2. **Clone** your fork locally:

```bash
gh repo fork kirill-latish/hlsa2-labs --clone
cd hlsa2-labs
```

3. Navigate to the lab directory and follow the lab's `README.md`.

## Requirements

- Python 3.12+
- Git

## Repository Structure

```
hlsa2-labs/
  README.md          ← you are here
  labs/
    1-1-latency-throughput-scaling-laws/
      README.md      ← lab setup, how to run, config reference
      simulator/     ← Python package (the code you benchmark and improve)
      scripts/       ← benchmark runner
      tests/         ← automated tests
      results/       ← your benchmark output (committed by you)
```

## License

Educational use only. All rights reserved.
