#!/usr/bin/env bash
# Deterministic guard for the canonical schema.sql files.
#
# ApplyTenantSchema / ApplyControlPlaneMigrations execute each file as ONE
# tx.Exec, so a single bad byte or stray character anywhere aborts the whole
# apply: no tenant provisions and no existing tenant boots. These checks are
# the cheap half of the net -- the CI schema-apply job runs the file against a
# real Postgres for the authoritative answer. Everything here is deterministic
# and costs no tokens.
#
# Wired as a PostToolUse hook on Edit|Write. Exit 2 tells Claude Code to
# surface stderr as blocking feedback.
set -uo pipefail

# The hook receives the tool payload as JSON on stdin.
payload=$(cat)
file=$(printf '%s' "$payload" | sed -n 's/.*"file_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

case "$file" in
  *database/migrations/*.sql) ;;
  *) exit 0 ;;
esac
[ -f "$file" ] || exit 0

fail=0
err () { echo "$1" >&2; fail=1; }

# 1. Encoding. Postgres rejects invalid byte sequences at the protocol level,
#    so this aborts the apply even when the bytes sit inside a -- comment.
if ! iconv -f UTF-8 -t UTF-8 "$file" >/dev/null 2>&1; then
  err "SCHEMA GUARD: $file is not valid UTF-8."
  bad=$(python3 - "$file" <<'PY' 2>/dev/null
import sys
p = sys.argv[1]
for i, line in enumerate(open(p, 'rb').read().split(b'\n'), 1):
    try:
        line.decode('utf-8')
    except UnicodeDecodeError:
        print("  line %d: %s" % (i, line[:60].decode('ascii', 'replace')))
PY
)
  [ -n "$bad" ] && printf '%s\n' "$bad" | head -5 >&2
  err "  Fix: re-save as UTF-8. Prefer plain ASCII (-- and ->) in comments;"
  err "       this file is executed, not rendered."
fi

# 2. Markdown headers. '#' is not a Postgres comment ('--' is), so a pasted
#    '### 5.2 `foo`' is a syntax error.
if grep -nE '^[[:space:]]*#' "$file" >/dev/null 2>&1; then
  err "SCHEMA GUARD: markdown header(s) in SQL -- '#' is not a Postgres comment."
  grep -nE '^[[:space:]]*#' "$file" | head -5 | sed 's/^/  /' >&2
  err "  Fix: use '-- N. name' to match the file's own convention."
fi

# 3. Statements that cannot run inside a transaction, or that destroy tenant
#    data. Mirrors migration-auditor rules 3-4, made mechanical.
#
#    ADDED LINES ONLY. The canonical file legitimately contains historical
#    drops (000012_drop_legacy_crm drops the old leads/prospects tables), so
#    scanning the whole file cries wolf on known-good content -- and a guard
#    that fires on the good file gets ignored. Checks 1 and 2 above are
#    whole-file because invalid UTF-8 and '#' headers are never legitimate.
if git rev-parse --git-dir >/dev/null 2>&1; then
  added=$(git diff HEAD --unified=0 -- "$file" 2>/dev/null \
    | grep '^+' | grep -v '^+++' | sed 's/^+//; s/--.*//' \
    | grep -nEi '\b(CREATE[[:space:]]+INDEX[[:space:]]+CONCURRENTLY|VACUUM|ALTER[[:space:]]+SYSTEM|DROP[[:space:]]+TABLE|DROP[[:space:]]+COLUMN|TRUNCATE)\b' \
    | head -5)
  if [ -n "$added" ]; then
    err "SCHEMA GUARD: transaction-unsafe or destructive statement(s) added:"
    printf '%s\n' "$added" | sed 's/^/  /' >&2
    err "  The whole file runs in one tx; these abort it or risk tenant data loss."
    err "  Recovery is via Neon PITR, never down-SQL. Discuss before removing anything."
  fi
fi

if [ "$fail" -ne 0 ]; then
  echo "" >&2
  echo "This file is applied whole, in one transaction, to EVERY tenant database." >&2
  exit 2
fi
exit 0
