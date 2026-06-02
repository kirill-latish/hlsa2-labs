#!/usr/bin/env python3
"""Replay the seeded event stream twice and assert that the consumer's
final state hash is identical.

Strategy:
  1. Reset accounts table to a known baseline.
  2. Run replay #1, hash state.
  3. Reset offsets, replay #2 over the SAME event stream.
  4. Hash again. idempotent mode -> identical; naive mode -> diff.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path


def run(cmd: list[str], **kw) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, check=True, text=True, **kw)


def docker_compose_exec(svc: str, *cmd: str, capture: bool = False) -> str:
    proc = subprocess.run(
        ["docker", "compose", "exec", "-T", svc] + list(cmd),
        check=True, text=True, capture_output=capture,
    )
    return (proc.stdout or "").strip()


def reset_payment_state() -> None:
    docker_compose_exec(
        "payment-pg", "psql", "-U", "payment", "-d", "payment", "-c",
        "TRUNCATE accounts; INSERT INTO accounts (user_id, balance) "
        "SELECT 'user-' || g, 0 FROM generate_series(1, 50) AS g; "
        "TRUNCATE processed_events;"
    )


def hash_state() -> str:
    return docker_compose_exec(
        "payment-pg", "psql", "-U", "payment", "-d", "payment", "-tAc",
        "SELECT md5(string_agg(user_id || ':' || balance, ',' ORDER BY user_id)) FROM accounts",
        capture=True,
    )


def reset_consumer_group() -> None:
    # Delete + recreate via consumer restart; rpk group delete fails if
    # no group exists yet, which is fine.
    subprocess.run(
        ["docker", "compose", "exec", "-T", "redpanda", "rpk", "group", "delete", "lab42-consumer"],
        check=False, capture_output=True,
    )


def restart_consumer(mode: str) -> None:
    subprocess.run(["docker", "compose", "stop", "consumer"], check=True)
    env = os.environ.copy()
    env["CONSUMER_MODE"] = mode
    subprocess.run(["docker", "compose", "up", "-d", "consumer"], check=True, env=env)
    # Wait for healthcheck.
    for _ in range(30):
        try:
            docker_compose_exec("consumer", "wget", "-qO-", "http://localhost:9103/healthz", capture=True)
            return
        except subprocess.CalledProcessError:
            import time
            time.sleep(1)


def seed(seed_n: int = 1) -> None:
    env = os.environ.copy()
    env["SEED"] = str(seed_n)
    env["WINDOW"] = os.environ.get("WINDOW", "30s")
    env["EVENT_RATE"] = os.environ.get("EVENT_RATE", "50")
    env["ORDERS"] = os.environ.get("ORDERS", "100")
    subprocess.run(["bash", "scripts/seed-events.sh"], check=True, env=env)


def wait_for_drain(seconds: int = 15) -> None:
    import time
    time.sleep(seconds)


def main() -> int:
    mode = os.environ.get("CONSUMER_MODE", "idempotent")
    out_dir = Path("perf/results/replay") / mode
    out_dir.mkdir(parents=True, exist_ok=True)

    print(f"\n[assert-idempotent] mode={mode}")
    reset_payment_state()
    restart_consumer(mode)
    reset_consumer_group()

    print("[assert-idempotent] replay #1: seed + drain")
    seed(seed_n=1)
    wait_for_drain(int(os.environ.get("REPLAY_WAIT_S", "15")))
    h1 = hash_state()
    print(f"  hash#1: {h1}")

    print("[assert-idempotent] replay #2: reset offsets + same seed")
    reset_consumer_group()
    restart_consumer(mode)
    seed(seed_n=1)
    wait_for_drain(int(os.environ.get("REPLAY_WAIT_S", "15")))
    h2 = hash_state()
    print(f"  hash#2: {h2}")

    summary = {
        "mode": mode,
        "hash_replay_1": h1,
        "hash_replay_2": h2,
        "match": h1 == h2,
    }
    (out_dir / "summary.json").write_text(json.dumps(summary, indent=2))

    print()
    if h1 == h2:
        print(f"PASS: identical state hashes after double-replay (mode={mode}).")
        return 0 if mode == "idempotent" else 1
    else:
        print(f"DIFF: state hashes differ after double-replay (mode={mode}).")
        return 0 if mode == "naive" else 1


if __name__ == "__main__":
    sys.exit(main())
