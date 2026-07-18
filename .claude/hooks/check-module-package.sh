#!/usr/bin/env bash
# Deterministic guard against the most mechanical clone-drift bug: a document
# module cloned from a sibling (e.g. estimate -> invoice) that still declares
# the donor's `package` name. The document modules are flat copy-paste twins,
# so a package clause that disagrees with its directory is *never* legitimate
# -- safe to check whole-file on every edit at zero tokens. This is the cheap
# front half of the module-drift-checker agent; the agent still owns the
# judgement calls (missing auth/scope/logging, other donor-name leftovers).
#
# Wired as a PostToolUse hook on Edit|Write|MultiEdit. Exit 2 tells Claude Code
# to surface stderr as blocking feedback.
set -uo pipefail

# The hook receives the tool payload as JSON on stdin.
payload=$(cat)
file=$(printf '%s' "$payload" | sed -n 's/.*"file_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

# Only Go files sitting directly inside a document-module directory.
case "$file" in
  *.go) ;;
  *) exit 0 ;;
esac
[ -f "$file" ] || exit 0

mod=$(basename "$(dirname "$file")")
case "$mod" in
  quote|estimate|salesorder|invoice|payment|creditmemo|vendors|inventory|refund) ;;
  *) exit 0 ;;
esac

# First real `package` clause. Empty means the file is mid-write / has no
# package line yet -- don't block on a transient state.
actual=$(grep -m1 -E '^package [A-Za-z0-9_]+' "$file" | awk '{print $2}')
[ -n "$actual" ] || exit 0

# Non-test files must be `package <dir>`. Test files may additionally be the
# black-box external test package `package <dir>_test` (Go allows it in-dir;
# e.g. invoice/*_test.go declares `package invoice_test`).
ok=0
[ "$actual" = "$mod" ] && ok=1
case "$file" in
  *_test.go) [ "$actual" = "${mod}_test" ] && ok=1 ;;
esac

if [ "$ok" -ne 1 ]; then
  echo "MODULE GUARD: $file declares 'package $actual' but sits in $mod/." >&2
  case "$file" in
    *_test.go) echo "  Expected 'package $mod' or 'package ${mod}_test'." >&2 ;;
    *)         echo "  Expected 'package $mod'." >&2 ;;
  esac
  echo "  Classic clone-drift leftover from copying a sibling module. Fix the" >&2
  echo "  package clause, then run the module-drift-checker agent for the rest." >&2
  exit 2
fi
exit 0
