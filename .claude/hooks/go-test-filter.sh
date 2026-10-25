#!/usr/bin/env bash
# PreToolUse hook (Bash): rewrites a plain `go test ...` command so its output
# is piped through filter-go-test.sh, keeping only failures. Exit status of
# go test is preserved via pipefail. Commands that already pipe, redirect, or
# ask for verbose/JSON/bench output are left alone.
set -uo pipefail

payload=$(cat)
cmd=$(printf '%s' "$payload" | jq -r '.tool_input.command // empty')

case "$cmd" in
  "go test"*) ;;
  *) exit 0 ;;
esac
# Only a single plain command: never rewrite (and auto-allow) chained input.
case "$cmd" in
  *"&"*|*";"*|*'$('*|*'`'*|*"|"*|*">"*|*" -v"*|*"-json"*|*"-bench"*|*"go test -c"*) exit 0 ;;
esac

filter="$CLAUDE_PROJECT_DIR/.claude/hooks/filter-go-test.sh"
[ -f "$filter" ] || exit 0

new="set -o pipefail; { $cmd ; } 2>&1 | bash \"$filter\""
jq -n --arg c "$new" '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"allow",updatedInput:{command:$c}}}'
