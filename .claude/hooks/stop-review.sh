#!/usr/bin/env bash
# Reviews the change on the current branch against AGENTS.md when Claude Code
# is about to end a turn.
#
# The script decides when a review runs and what a verdict means. The reviewer
# is a separate headless Claude Code session that only reads the change and
# lists violations.
#
# Reads the hook payload from stdin. Exits 0 without a review when a
# background task is still running, when no Go source, go.mod, template, or
# migration file changed, or when this exact change was already approved.
# Exits 2 with the violations on stderr when the reviewer finds any, which
# sends Claude back to fix them.
#
# When the payload says a stop hook already continued this turn, only
# violations of the Security section block again, so that one disputed
# finding cannot keep the turn from ending.
#
# A reviewer that cannot run or gives no readable verdict never blocks: the
# script prints a systemMessage for the user on stdout and exits 0.
#
# HOOK_REVIEW_CMD replaces the reviewer command. It reads the prompt and the
# change on stdin and prints the verdict JSON on stdout.
set -u

payload="$(cat)"

hooks_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null)}"
[ -n "$root" ] || exit 0
cd "$root" || exit 0

if ! command -v jq >/dev/null 2>&1; then
  echo "stop-review: jq is required to read the hook input; not reviewing" >&2
  exit 0
fi

# skip_review MESSAGE tells the user why no verdict was reached and lets the
# turn end.
skip_review() {
  jq -n --arg m "stop-review: $1; ending the turn without a rules review." '{systemMessage: $m}'
  exit 0
}

if printf '%s' "$payload" | jq -e '[.background_tasks[]? | select(.status == "running")] | length > 0' >/dev/null 2>&1; then
  exit 0
fi

base="HEAD"
if git rev-parse -q --verify refs/heads/main >/dev/null 2>&1; then
  base="$(git merge-base HEAD main 2>/dev/null)" || base="HEAD"
fi
git rev-parse -q --verify "$base^{commit}" >/dev/null 2>&1 || exit 0

reviewable=('*.go' 'go.mod' '*.html' '*.tmpl' '*.sql')
changed="$(
  git diff --name-only "$base" -- "${reviewable[@]}"
  git ls-files --others --exclude-standard -- "${reviewable[@]}"
)"
[ -n "$changed" ] || exit 0

change="$(
  git diff "$base"
  git ls-files -z --others --exclude-standard | while IFS= read -r -d '' file; do
    git diff --no-index -- /dev/null "$file"
  done
)"

approved_file="$(git rev-parse --git-dir)/stop-review-approved"
change_id="$(printf '%s' "$change" | git hash-object --stdin)"
if [ -f "$approved_file" ] && [ "$(cat "$approved_file")" = "$change_id" ]; then
  exit 0
fi

schema='{
  "type": "object",
  "properties": {
    "violations": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "file": {"type": "string"},
          "line": {"type": "integer"},
          "section": {"type": "string"},
          "fix": {"type": "string"}
        },
        "required": ["file", "line", "section", "fix"],
        "additionalProperties": false
      }
    }
  },
  "required": ["violations"],
  "additionalProperties": false
}'

# run_reviewer reads the prompt and the change on stdin and prints the verdict
# JSON. The headless session runs with hooks off, so it cannot start this hook
# again, and with read-only tools, so it never waits for a permission.
run_reviewer() {
  if [ -n "${HOOK_REVIEW_CMD:-}" ]; then
    bash -c "$HOOK_REVIEW_CMD"
    return
  fi
  claude -p \
    --model sonnet \
    --setting-sources project \
    --strict-mcp-config \
    --settings '{"disableAllHooks": true}' \
    --tools "Read,Grep,Glob" \
    --no-session-persistence \
    --output-format json \
    --json-schema "$schema"
}

if ! verdict="$({
  cat "$hooks_dir/review-prompt.md"
  printf '\n## The change\n\n'
  printf '%s\n' "$change"
} | run_reviewer 2>/dev/null)"; then
  skip_review "the reviewer could not run"
fi

active="$(printf '%s' "$payload" | jq -r '.stop_hook_active == true')"
if ! violations="$(printf '%s' "$verdict" | jq -r --argjson active "$active" '
  .structured_output.violations
  | if type != "array" then error("no verdict") else . end
  | map(select(($active | not) or .section == "Security"))
  | .[:10][]
  | "\(.file):\(.line) — \(.section) — \(.fix)"
' 2>/dev/null)"; then
  skip_review "the reviewer gave no readable verdict"
fi

if [ -z "$violations" ]; then
  printf '%s\n' "$change_id" > "$approved_file"
  exit 0
fi

{
  echo "stop-review: the change breaks rules in AGENTS.md; fix these before finishing"
  printf '%s\n' "$violations"
} >&2
exit 2
