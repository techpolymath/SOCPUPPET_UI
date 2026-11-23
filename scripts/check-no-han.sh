#!/usr/bin/env bash
# Reject Han-script text in tracked and untracked source, tests, documentation,
# and fixtures. Ignored/generated files and binary assets are excluded.
set -euo pipefail
cd "$(dirname "$0")/.."

pattern='[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}\x{20000}-\x{2FA1F}]'
mapfile -d '' files < <(git ls-files -z --cached --others --exclude-standard)
if matches=$(LC_ALL=C.UTF-8 grep -I -nP "$pattern" -- "${files[@]}"); then
  printf 'Han-script text is not allowed in SOCPUPPET_UI source files:\n%s\n' "$matches" >&2
  exit 1
fi

printf 'No Han-script text found in SOCPUPPET_UI source files.\n'
