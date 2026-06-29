#!/usr/bin/env bash
# inject-origin-outage - show stale-if-error graceful degradation. With
# a short TTL we warm an object, let it expire, then take the origin down
# and request it again: with stale_if_error on, the PoP serves the
# last-known-good STALE copy instead of erroring.
#
# Run `make apply-fix CANDIDATE=stale-if-error` first.
# Inputs (env): LABEL.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

LABEL="${LABEL:-stale-if-error}"
OBJ="/obj/s2"
OUT_DIR="perf/results/${LABEL}"
mkdir -p "${OUT_DIR}"
SAMPLES="${OUT_DIR}/stale-samples.txt"

status_of() { curl -fsS -D - -o /dev/null "$@" | awk 'tolower($1)=="x-cache-status:"{print $2}' | tr -d '\r'; }

echo "[inject-origin-outage] LABEL=${LABEL} object=${OBJ}"

# Short TTL on the PoPs so the entry expires quickly, and stale_if_error
# on so the expired entry can be served during the outage.
push_config_pops '{"ttl_seconds":5,"stale_if_error":true}'

# Warm, then wait for the entry to expire.
curl -fsS -o /dev/null "${POP1_URL}${OBJ}" || true
warm="$(status_of "${POP1_URL}${OBJ}")"   # should be HIT
echo "warm request: ${warm}" | tee "${SAMPLES}"
sleep 7

snapshot_metrics "${OUT_DIR}" before

# Take the origin down and request the now-expired object.
push_config_origin '{"outage":true}'
sleep 1
stale_count=0
{
  echo "# origin outage injected $(date -u +%Y-%m-%dT%H:%M:%SZ)"
} >>"${SAMPLES}"
for _ in $(seq 1 10); do
  st="$(status_of "${POP1_URL}${OBJ}")"
  echo "during outage: ${st}" >>"${SAMPLES}"
  if [[ "${st}" == "STALE" ]]; then
    stale_count=$((stale_count + 1))
  fi
done

snapshot_metrics "${OUT_DIR}" after

# Restore: origin back up, default TTL.
push_config_origin '{"outage":false}'
push_config_pops '{"ttl_seconds":60}'

jq -n --arg label "${LABEL}" --arg object "${OBJ}" --argjson stale "${stale_count}" \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{label:$label, object:$object, stale_served:$stale, samples:10, captured_at:$captured_at}' \
  >"${OUT_DIR}/summary.json"

cat "${SAMPLES}"
echo "[inject-origin-outage] stale responses served during outage: ${stale_count}/10"
echo "[inject-origin-outage] wrote ${OUT_DIR}/ (origin restored, TTL reset)."
