#!/usr/bin/env bash
# Show the topic + its partitions + the consumer group offsets/lag via
# rpk. Passes (exit 0) when the topic exists with >= 1 partition.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

log_step "topic ${TOPIC} describe"
rpk topic describe "${TOPIC}" || { echo "FAIL: topic ${TOPIC} not found"; exit 1; }

log_step "partitions"
rpk topic describe "${TOPIC}" -p || true

log_step "consumer group ${GROUP}"
rpk group describe "${GROUP}" 2>/dev/null || echo "(group not yet formed - start the producer first)"

# Assert the topic has at least one partition.
parts="$(rpk topic describe "${TOPIC}" 2>/dev/null | awk '/PARTITIONS/{print $2}' | head -1)"
[[ -z "${parts}" ]] && parts="$(rpk topic list 2>/dev/null | awk -v t="${TOPIC}" '$1==t{print $2}')"
echo
if [[ "${parts:-0}" -ge 1 ]]; then
  echo "OK: topic ${TOPIC} has ${parts} partition(s)."
else
  echo "FAIL: could not confirm partitions for ${TOPIC}."
  exit 1
fi
