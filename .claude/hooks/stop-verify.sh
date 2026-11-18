#!/usr/bin/env bash
# Runs the full check when Claude Code is about to end a turn.
#
# Reads the hook payload from stdin. Exits 0 when nothing that affects the
# build has changed or the check passes. Exits 2 with the tail of the check
# output on stderr when the check fails, which sends Claude back to fix it.
# When the payload says a stop hook already blocked this turn, a failure is
# reported on stderr and the script exits 0 so the turn cannot loop forever.
set -u

payload="$(cat)"

root="${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null)}"
[ -n "$root" ] || exit 0
cd "$root" || exit 0

changed="${HOOK_CHANGED_FILES-$(git status --porcelain | cut -c4-)}"
if ! printf '%s\n' "$changed" |
  grep -Eq '\.go"?$|(^|/)go\.(mod|sum)$|\.golangci\.yml$|(^|/)Makefile$|\.tmpl"?$|\.html"?$'; then
  exit 0
fi

check="${HOOK_CHECK_CMD:-make check}"
if check_output="$(bash -c "$check" 2>&1)"; then
  exit 0
fi

if [ "$(printf '%s' "$payload" | jq -r '.stop_hook_active // false')" = "true" ]; then
  {
    echo "stop-verify: checks are still failing; reporting instead of retrying"
    printf '%s\n' "$check_output" | tail -n 40
  } >&2
  exit 0
fi

{
  echo "stop-verify: '$check' failed; fix the findings below before finishing"
  printf '%s\n' "$check_output" | tail -n 40
} >&2
exit 2
