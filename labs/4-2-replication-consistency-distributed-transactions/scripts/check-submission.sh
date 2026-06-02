#!/usr/bin/env bash
# Walk docs/review.md and assert every cited filename actually exists.
# Also surface remaining TODO markers.
set -euo pipefail
LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REVIEW="${LAB_ROOT}/docs/review.md"

if [ ! -f "${REVIEW}" ]; then
  echo "missing ${REVIEW} - copy docs/review.template.md first"
  exit 2
fi

echo "[check-submission] scanning ${REVIEW}"
missing=0

# Match file paths under perf/results/, docs/img/, runbooks/, docs/.
# Anchored on `perf/results/`, `docs/img/`, `runbooks/`, `docs/`.
patterns=(
  'perf/results/[^[:space:]`)*]+'
  'docs/img/[^[:space:]`)*]+'
  'runbooks/[^[:space:]`)*]+'
  'docs/[^[:space:]`)*]*\.md'
)

while IFS= read -r path; do
  full="${LAB_ROOT}/${path}"
  if [ ! -e "${full}" ]; then
    echo "  MISSING: ${path}"
    missing=$((missing + 1))
  fi
done < <(grep -E -h -o "$(IFS='|'; echo "${patterns[*]}")" "${REVIEW}" | sort -u)

todo_lines=$(grep -nE 'TODO|TBD|FIXME' "${REVIEW}" || true)
if [ -n "${todo_lines}" ]; then
  echo
  echo "[check-submission] remaining TODO markers in ${REVIEW}:"
  echo "${todo_lines}"
fi

if [ "${missing}" -gt 0 ]; then
  echo
  echo "[check-submission] FAIL: ${missing} cited file(s) missing"
  exit 1
fi

echo
echo "[check-submission] OK: every cited path exists"
