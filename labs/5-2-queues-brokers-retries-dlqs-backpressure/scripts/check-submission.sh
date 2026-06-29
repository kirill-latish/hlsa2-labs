#!/usr/bin/env bash
# check-submission - verify every backtick-quoted filename in
# docs/review.md exists. Same rubric discipline as the other labs
# ("every claim cites an artifact").

set -uo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"

REVIEW="docs/review.md"
if [[ ! -f "${REVIEW}" ]]; then
  echo "FAIL: ${REVIEW} does not exist. Copy docs/review.template.md to docs/review.md and fill it in."
  exit 1
fi

MISSING=0
TOTAL=0

CANDIDATES_STR="$(
  grep -oE '`[A-Za-z0-9_./-]+`' "${REVIEW}" \
    | sed 's/^`//; s/`$//' \
    | sort -u \
    | awk '/[\/]|\.json$|\.png$|\.md$|\.txt$|\.csv$|\.jpg$/'
)"

while IFS= read -r raw; do
  [[ -z "${raw}" ]] && continue
  TOTAL=$((TOTAL + 1))
  if [[ -e "${raw}" ]]; then
    continue
  fi
  if compgen -G "${raw}" >/dev/null; then
    continue
  fi
  echo "MISSING: ${raw}"
  MISSING=$((MISSING + 1))
done <<< "${CANDIDATES_STR}"

echo
if [[ "${MISSING}" -gt 0 ]]; then
  echo "FAIL: ${MISSING} / ${TOTAL} cited artefacts not found under the lab."
  exit 1
fi
echo "OK: ${TOTAL} cited artefacts all exist."

WORDS="$(wc -w < "${REVIEW}" | tr -d '[:space:]')"
echo "review.md word count: ${WORDS}"
if [[ "${WORDS}" -lt 1200 ]]; then
  echo "WARN: review is under 1,200 words. The rubric expects ~1,500-2,000."
fi
if [[ "${WORDS}" -gt 2500 ]]; then
  echo "WARN: review is over 2,500 words. Tighten it - the rubric expects ~1,500-2,000."
fi

if grep -q 'TODO' "${REVIEW}"; then
  echo "WARN: docs/review.md still contains TODO markers."
fi
