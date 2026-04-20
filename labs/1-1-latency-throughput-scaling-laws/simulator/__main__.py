"""Entry point for ``python -m simulator``."""

import sys
from pathlib import Path

from simulator.runner import run_benchmark


def main() -> None:
    config_path = None
    if len(sys.argv) > 1:
        config_path = Path(sys.argv[1])
    run_benchmark(config_path)


if __name__ == "__main__":
    main()
