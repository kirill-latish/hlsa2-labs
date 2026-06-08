#!/usr/bin/env bash
# Verifies every filesystem path cited in docs/review.md exists.
# Mirrors lab 4-2's submission checker.
set -euo pipefail
LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REVIEW="${LAB_ROOT}/docs/review.md"

if [[ ! -f "${REVIEW}" ]]; then
  echo "[lab43] no docs/review.md yet (copy docs/review.template.md -> docs/review.md)"
  exit 1
fi

missing=0
while IFS= read -r path; do
  full="${LAB_ROOT}/${path}"
  if [[ ! -e "${full}" ]]; then
    echo "[lab43] MISSING: ${path}"
    missing=$(( missing + 1 ))
  fi
done < <(grep -oE 'perf/results/[^ )"]+|docs/[^ )"]+\.(md|json|csv|txt)|runbooks/[^ )"]+\.md' "${REVIEW}" | sort -u)

if [[ "${missing}" -gt 0 ]]; then
  echo "[lab43] ${missing} missing path(s) in review.md"
  exit 1
fi

echo "[lab43] review.md path check OK"
