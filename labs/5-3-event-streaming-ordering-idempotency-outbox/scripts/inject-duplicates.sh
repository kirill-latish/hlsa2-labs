#!/usr/bin/env bash
# inject-duplicates RATE=20pct - set the producer's duplicate-injection
# rate and drive a burst so the consumer's dedup path is exercised. This
# also resets the side-effect / dedup / projection tables so the
# following verify-exactly-once measures THIS phase cleanly.
#
# Env / make vars:
#   RATE      duplicate rate (20pct | 0.2 | 20)   (default 20pct)
#   DURATION  burst length    (default 60s)
#   RATE_EPS  events/s        (default 200)
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

RAW="${RATE:-20pct}"
DUR="$(to_seconds "${DURATION:-60s}")"
RATE_EPS="${RATE_EPS:-200}"

# Normalise the rate into a 0..1 fraction.
frac="$(python3 -c "
v='${RAW}'.strip().lower()
if v.endswith('pct'): v=v[:-3]; print(float(v)/100.0); 
elif v.endswith('%'): print(float(v[:-1])/100.0)
else:
    f=float(v); print(f/100.0 if f>1 else f)
")"

log_step "reset side_effects / processed_ids / projection for a clean phase"
psql_exec "TRUNCATE side_effects, processed_ids, projection RESTART IDENTITY;"

log_step "inject-duplicates RATE=${RAW} (fraction=${frac}) for ${DUR}s"
producer_config "{\"publish_mode\":\"direct\",\"key_strategy\":\"entity\",\"duplicate_rate\":${frac}}"
producer_run "${RATE_EPS}" "${DUR}" "duplicates-${RAW}"
wait_consumer_drained 90

emitted="$(curl -fsS "${PRODUCER}/summary" 2>/dev/null | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("duplicates",0))' 2>/dev/null || echo '?')"
echo
echo "inject-duplicates: ok. injected duplicates (producer) ~ ${emitted}"
