#!/usr/bin/env bash
# Run a single load-generator experiment and timestamp the artifacts.
#
# Usage:
#   bash scripts/run-experiment.sh <profile>
#
# <profile> is one of: nominal | fast-burn | slow-burn | smoke (see loadgen/profiles.yaml).
#
# This is a thin wrapper around `docker compose run loadgen` that:
#   1. Confirms the stack is up,
#   2. Records start/stop timestamps for the experiment,
#   3. Saves loadgen stdout to artifacts/02-experiments/<profile>-<ts>.log
#      and a tiny meta.json describing the run.

set -euo pipefail

PROFILE="${1:-}"
if [ -z "${PROFILE}" ]; then
  echo "Usage: $0 <profile>" >&2
  echo "  profile: nominal | fast-burn | slow-burn | smoke" >&2
  exit 2
fi

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"

if ! docker compose ps --format json checkout >/dev/null 2>&1; then
  echo "Stack is not up. Run 'docker compose up -d' first." >&2
  exit 3
fi

TS="$(date -u +%Y%m%dT%H%M%SZ)"
case "${PROFILE}" in
  nominal|smoke) ART_DIR="artifacts/01-baseline" ;;
  fast-burn|slow-burn) ART_DIR="artifacts/02-experiments" ;;
  *) ART_DIR="artifacts/02-experiments" ;;
esac
mkdir -p "${ART_DIR}"

LOG_FILE="${ART_DIR}/${PROFILE}-${TS}.log"
META_FILE="${ART_DIR}/${PROFILE}-${TS}.meta.json"

CAPTURE="${LAB_ROOT}/scripts/capture-metrics.sh"

START_EPOCH="$(date -u +%s)"
START_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "[run-experiment] profile=${PROFILE} start=${START_ISO}"
echo "[run-experiment] log=${LOG_FILE}"

# "before" snapshot: proves the SLO pipeline was quiet (or not) going in.
bash "${CAPTURE}" snapshot "${ART_DIR}" "${PROFILE}-${TS}-before" || \
  echo "[run-experiment] WARNING: before-snapshot failed" >&2

# --use-aliases attaches the `loadgen` network alias to the one-off run
# container. Without it `docker compose run` joins the network unaliased,
# Prometheus cannot resolve loadgen:9999, and the client-side metrics the
# load generator exposes are silently never scraped - the target simply
# sits DOWN for the whole experiment.
docker compose run --rm --use-aliases loadgen python -m loadgen "${PROFILE}" 2>&1 | tee "${LOG_FILE}"

END_EPOCH="$(date -u +%s)"
END_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
DURATION=$((END_EPOCH - START_EPOCH))

# "after" snapshot, then the range series the TTD/TTR numbers are read
# from. The range call sleeps first so the post-injection decay and the
# alert's resolve transition land inside the captured window.
bash "${CAPTURE}" snapshot "${ART_DIR}" "${PROFILE}-${TS}-after" || \
  echo "[run-experiment] WARNING: after-snapshot failed" >&2

RANGE_WAIT="${RANGE_WAIT:-300}"
echo "[run-experiment] waiting ${RANGE_WAIT}s for alert decay before range capture"
sleep "${RANGE_WAIT}"
bash "${CAPTURE}" range "${ART_DIR}" "${PROFILE}-${TS}" \
  "${START_EPOCH}" "${END_EPOCH}" || \
  echo "[run-experiment] WARNING: range capture failed" >&2

cat > "${META_FILE}" <<EOF
{
  "profile": "${PROFILE}",
  "start_iso": "${START_ISO}",
  "end_iso": "${END_ISO}",
  "duration_seconds": ${DURATION},
  "log_path": "${LOG_FILE}"
}
EOF

echo "[run-experiment] done. profile=${PROFILE} duration=${DURATION}s"
echo "[run-experiment] meta=${META_FILE}"
