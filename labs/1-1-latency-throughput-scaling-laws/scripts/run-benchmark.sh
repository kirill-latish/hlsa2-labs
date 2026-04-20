#!/usr/bin/env bash
# Run the latency/throughput/scaling benchmark.
# Usage: bash scripts/run-benchmark.sh [path/to/config.yaml]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LAB_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$LAB_DIR"

# Activate venv if present
if [ -d ".venv/bin" ]; then
    source .venv/bin/activate
elif [ -d "venv/bin" ]; then
    source venv/bin/activate
fi

python3 -m simulator "$@"
