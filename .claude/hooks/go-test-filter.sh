#!/usr/bin/env bash
# PreToolUse hook (Bash): rewrites a plain `go test ...` command so its output
# is piped through filter-go-test.sh, keeping only failures. Exit status of
# go test is preserved via pipefail. Only commands matching a strict allowlist
# of flags and package paths are rewritten; everything else is left alone.
set -uo pipefail

payload=$(cat)
cmd=$(printf '%s' "$payload" | jq -r '.tool_input.command // empty')

# Strict allowlist: a single-line `go test` whose every token is a known-safe
# flag or a plain package path. Anything else (newlines, shell metacharacters,
# -exec/-toolexec/-o/-c, quoted or space-separated flag values) is left alone.
flag='-(race|short|failfast|cover)|-(count|timeout|run|tags|p|parallel)=[A-Za-z0-9_.,:-]+'
pkg='[A-Za-z0-9_./][A-Za-z0-9_./-]*'
re="^go test( ($flag|$pkg))*$"
[[ "$cmd" =~ $re ]] || exit 0

filter="$CLAUDE_PROJECT_DIR/.claude/hooks/filter-go-test.sh"
[ -f "$filter" ] || exit 0

new="set -o pipefail; { $cmd ; } 2>&1 | bash \"$filter\""
jq -n --arg c "$new" '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"allow",updatedInput:{command:$c}}}'
