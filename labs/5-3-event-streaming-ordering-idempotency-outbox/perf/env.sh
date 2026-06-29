#!/usr/bin/env bash
# Capture the environment fingerprint students cite in their review.
# Writes perf/results/env.txt (human-readable) + perf/results/meta.json
# (machine-readable): broker version, partition count, consumer-group
# size, Postgres version, and inter-node RTTs (producer->broker,
# broker->consumer, consumer->postgres).
set -uo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"

OUT_TXT="perf/results/env.txt"
OUT_JSON="perf/results/meta.json"
mkdir -p "$(dirname "${OUT_TXT}")"

ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
TOPIC="${EVENTS_TOPIC:-order-events}"
GROUP="${CONSUMER_GROUP:-lab53-consumers}"

dc() { docker compose "$@" 2>/dev/null; }

host_uname="$(uname -a 2>/dev/null || echo unknown)"
docker_v="$(docker --version 2>/dev/null || echo 'docker NOT FOUND')"
compose_v="$(docker compose version 2>/dev/null | head -1 || echo 'compose NOT FOUND')"

# Broker version + partition count.
broker_v="$(dc exec -T redpanda rpk version 2>/dev/null | head -1 || echo unknown)"
partitions="$(dc exec -T redpanda rpk topic describe "${TOPIC}" -p 2>/dev/null | grep -c -E '^[[:space:]]*[0-9]+' || echo 0)"
[[ "${partitions}" == "0" ]] && partitions="$(dc exec -T redpanda rpk topic list 2>/dev/null | awk -v t="${TOPIC}" '$1==t{print $2}' || echo unknown)"

# Consumer-group size (members).
consumers="$(dc exec -T redpanda rpk group describe "${GROUP}" 2>/dev/null | awk '/MEMBER/{f=1;next} f&&NF{c++} END{print c+0}' || echo 0)"
[[ -z "${consumers}" ]] && consumers=0

pg_v="$(dc exec -T postgres psql -U lab53 -d lab53 -tAc 'SHOW server_version' 2>/dev/null | tr -d '[:space:]' || echo unknown)"

# Inter-node RTTs (ms) using a busybox ping from inside the network.
rtt() {
  local from="$1" to="$2"
  dc exec -T "${from}" sh -c "ping -c 3 -W 1 ${to} 2>/dev/null | tail -1" 2>/dev/null \
    | sed -nE 's#.*= [0-9.]+/([0-9.]+)/.*#\1#p' || true
}
rtt_pb="$(rtt producer redpanda)"; rtt_pb="${rtt_pb:-n/a}"
rtt_bc="$(rtt consumer-1 redpanda)"; rtt_bc="${rtt_bc:-n/a}"
rtt_cp="$(rtt consumer-1 postgres)"; rtt_cp="${rtt_cp:-n/a}"

{
  echo "# Lab 5-3 environment fingerprint"
  echo "# Captured: ${ts}"
  echo
  echo "## Host / tooling"
  echo "- uname: ${host_uname}"
  echo "- ${docker_v}"
  echo "- ${compose_v}"
  echo
  echo "## Pipeline"
  echo "- broker: ${broker_v}"
  echo "- topic: ${TOPIC}"
  echo "- partitions: ${partitions}"
  echo "- consumer_group: ${GROUP}"
  echo "- consumers (members): ${consumers}"
  echo "- postgres: ${pg_v}"
  echo
  echo "## Inter-node RTT (avg ms)"
  echo "- producer -> redpanda: ${rtt_pb}"
  echo "- redpanda  -> consumer: ${rtt_bc}"
  echo "- consumer -> postgres: ${rtt_cp}"
  echo
  echo "## Lab git"
  if git rev-parse HEAD >/dev/null 2>&1; then
    echo "- commit: $(git rev-parse HEAD)"
    echo "- branch: $(git rev-parse --abbrev-ref HEAD)"
  else
    echo "- not a git checkout"
  fi
} | tee "${OUT_TXT}" >/dev/null

python3 - "$ts" "$broker_v" "$partitions" "$GROUP" "$consumers" "$pg_v" "$rtt_pb" "$rtt_bc" "$rtt_cp" >"${OUT_JSON}" <<'PY'
import json, sys, subprocess
ts, broker, parts, group, consumers, pg, rtt_pb, rtt_bc, rtt_cp = sys.argv[1:10]
def safe(cmd, default="unknown"):
    try:
        return subprocess.check_output(cmd, shell=True, text=True, stderr=subprocess.DEVNULL).strip()
    except Exception:
        return default
def num(x):
    try:
        return int(x)
    except Exception:
        try:
            return float(x)
        except Exception:
            return x
d = {
  "captured_at": ts,
  "broker_version": broker,
  "topic": "order-events",
  "partitions": num(parts),
  "consumer_group": group,
  "consumers": num(consumers),
  "postgres_version": pg,
  "rtt_ms": {
    "producer_to_broker": num(rtt_pb),
    "broker_to_consumer": num(rtt_bc),
    "consumer_to_postgres": num(rtt_cp),
  },
  "git": {
    "commit": safe("git rev-parse HEAD 2>/dev/null"),
    "branch": safe("git rev-parse --abbrev-ref HEAD 2>/dev/null"),
  },
}
print(json.dumps(d, indent=2))
PY

echo "Wrote ${OUT_TXT} and ${OUT_JSON}"
