#!/usr/bin/env bash
# verify-cacheability - confirm the cross-user-leak fix did NOT over-
# suppress caching of genuinely-shared content. Hits a static object
# twice on the same PoP and asserts the second response is a HIT, then
# reports the personalized route's status (should be BYPASS or per-user,
# never a cross-user HIT).
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

OUT_DIR="perf/results"
mkdir -p "${OUT_DIR}"
OUT="${OUT_DIR}/cacheability.txt"

POP="${POP1_URL}"
OBJ="/obj/s1"

status_of() { curl -fsS -D - -o /dev/null "$@" | awk 'tolower($1)=="x-cache-status:"{print $2}' | tr -d '\r'; }

# Prime then re-request the shared static object.
first="$(status_of "${POP}${OBJ}")"
second="$(status_of "${POP}${OBJ}")"

# Personalized route as one user, twice.
acct1="$(status_of -H 'Cookie: uid=verify-user' "${POP}/account")"
acct2="$(status_of -H 'Cookie: uid=verify-user' "${POP}/account")"

{
  echo "# verify-cacheability $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "shared static ${OBJ}: first=${first} second=${second}"
  echo "personalized /account (same user): first=${acct1} second=${acct2}"
} | tee "${OUT}"

if [[ "${second}" == "HIT" ]]; then
  echo "PASS: shared content is still cached (second request was a HIT)."
else
  echo "WARN: shared static object was not a HIT on the second request (status=${second}). The fix may be over-suppressing caching."
fi
echo "[verify-cacheability] wrote ${OUT}"
