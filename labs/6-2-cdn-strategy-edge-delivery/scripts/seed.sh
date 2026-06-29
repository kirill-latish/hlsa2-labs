#!/usr/bin/env bash
# seed - warm an initial set of popular objects on every PoP so the
# catalog is populated and the first baseline run isn't measuring a
# stone-cold cache. Idempotent: safe to run repeatedly.
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"
# shellcheck disable=SC1091
source "${LAB_ROOT}/scripts/_lib.sh"

POPULAR_STATIC=(s0 s1 s2 s3 s4 s5 s6 s7 s8 s9)
PAGES=(p0 p1 p2 p3 p4)

echo "[seed] warming popular objects on $((${#POP_URLS[@]})) PoPs..."
for u in "${POP_URLS[@]}"; do
  for id in "${POPULAR_STATIC[@]}"; do
    curl -fsS -o /dev/null "${u}/obj/${id}" || true
  done
  for id in "${PAGES[@]}"; do
    curl -fsS -o /dev/null "${u}/page/${id}" || true
  done
  echo "[seed]   ${u}: $(curl -fsS "${u}/admin/config" | jq -r '.cache_entries') entries"
done

echo "[seed] done. Catalog warmed: ${#POPULAR_STATIC[@]} static + ${#PAGES[@]} pages per PoP."
