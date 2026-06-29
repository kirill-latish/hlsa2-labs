#!/usr/bin/env bash
# apply-fix - reconfigure the edge health check via POST /admin/config.
#
# CANDIDATE:
#   fast-healthcheck    -> shallow check, tighter interval + threshold
#                          (faster failover detection; more check load)
#   deep-healthcheck    -> deep check (queries Postgres); the trap that
#                          turns a dependency blip into a full outage
#   shallow-healthcheck -> shallow check (process up only); rides out the
#                          dependency blip
#
# Inputs (env):
#   CANDIDATE  (required)
#   INTERVAL   health-check interval, accepts 2s/500ms-as-ms? use s (default 2s)
#   THRESHOLD  consecutive failures before down (default 2)
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
if [[ -f .env ]]; then set -a; source .env; set +a; fi

CANDIDATE="${CANDIDATE:?CANDIDATE=fast-healthcheck|deep-healthcheck|shallow-healthcheck is required}"
INTERVAL="${INTERVAL:-2s}"
THRESHOLD="${THRESHOLD:-2}"
EDGE="http://localhost:${LAB_EDGE_PORT:-8080}"

# Convert INTERVAL (e.g. 2s / 500ms / 2000) to milliseconds.
case "${INTERVAL}" in
  *ms) INTERVAL_MS="${INTERVAL%ms}" ;;
  *s)  INTERVAL_MS=$(( ${INTERVAL%s} * 1000 )) ;;
  *)   INTERVAL_MS="${INTERVAL}" ;;
esac

case "${CANDIDATE}" in
  fast-healthcheck)    DEPTH="shallow" ;;
  deep-healthcheck)    DEPTH="deep" ;;
  shallow-healthcheck) DEPTH="shallow" ;;
  *)
    echo "ERROR: CANDIDATE must be fast-healthcheck|deep-healthcheck|shallow-healthcheck" >&2
    exit 2 ;;
esac

BODY="$(jq -n --arg d "${DEPTH}" --argjson i "${INTERVAL_MS}" --argjson t "${THRESHOLD}" \
        '{health_depth:$d, health_interval_ms:$i, failure_threshold:$t}')"
echo "[apply-fix] CANDIDATE=${CANDIDATE} -> ${BODY}"
curl -fsS -X POST -H 'content-type: application/json' -d "${BODY}" "${EDGE}/admin/config"
echo
