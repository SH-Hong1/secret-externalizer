#!/usr/bin/env bash
# Runs the full check when Claude Code is about to end a turn.
#
# Reads the hook payload from stdin and runs the check every time, whether or
# not the working tree has uncommitted changes. Exits 0 when the check passes.
# Exits 2 with the tail of the check output on stderr when it fails, which
# sends Claude back to fix it.
#
# When the payload says a stop hook already continued this turn, a failure
# does not block again: the script prints a systemMessage for the user on
# stdout and exits 0, so the turn cannot loop forever.
set -u

payload="$(cat)"

root="${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null)}"
[ -n "$root" ] || exit 0
cd "$root" || exit 0

check="${HOOK_CHECK_CMD:-make check}"
if check_output="$(bash -c "$check" 2>&1)"; then
  exit 0
fi
summary="$(printf '%s\n' "$check_output" | tail -n 40)"

if ! command -v jq >/dev/null 2>&1; then
  {
    echo "stop-verify: '$check' failed, and jq is required to read the hook input; not blocking"
    printf '%s\n' "$summary"
  } >&2
  exit 0
fi

if [ "$(printf '%s' "$payload" | jq -r '.stop_hook_active // false')" = "true" ]; then
  jq -n --arg m "stop-verify: '$check' is still failing after a retry; ending the turn without a passing check.
$summary" '{systemMessage: $m}'
  exit 0
fi

{
  echo "stop-verify: '$check' failed; fix the findings below before finishing"
  printf '%s\n' "$summary"
} >&2
exit 2
