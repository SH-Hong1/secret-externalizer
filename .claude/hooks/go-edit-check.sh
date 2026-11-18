#!/usr/bin/env bash
# Checks a Go file right after Claude Code edits or writes it.
#
# Reads the hook payload from stdin and takes the file from
# .tool_input.file_path. Exits 0 without output when the file is not subject
# to checking or passes. Exits 2 with the reason on stderr when the file is
# not gofumpt-formatted or its package has lint findings.
set -u

if ! command -v jq >/dev/null 2>&1; then
  echo "go-edit-check: jq is required to read the hook input; install jq" >&2
  exit 2
fi

file="$(jq -r '.tool_input.file_path // empty')"
[ -n "$file" ] || exit 0

case "$file" in
  *.go) ;;
  *) exit 0 ;;
esac
[ -f "$file" ] || exit 0

root="${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null)}"
[ -n "$root" ] || exit 0
root="$(cd "$root" && pwd -P)"

dir="$(cd "$(dirname "$file")" && pwd -P)"
case "$dir/" in
  "$root"/*) ;;
  *) exit 0 ;;
esac
case "$dir/" in
  */testdata/*) exit 0 ;;
esac
file="$dir/$(basename "$file")"

bin="${HOOK_BIN_DIR:-$root/bin}"
if [ ! -x "$bin/gofumpt" ] || [ ! -x "$bin/golangci-lint" ]; then
  echo "go-edit-check: gofumpt or golangci-lint is missing in $bin; run 'make tools' first" >&2
  exit 2
fi

rel="${file#"$root"/}"
if [ -n "$("$bin/gofumpt" -extra -l "$file")" ]; then
  echo "gofumpt: $rel is not formatted; run 'make fmt'" >&2
  exit 2
fi

rel_dir="${dir#"$root"}"
if ! lint_output="$(cd "$root" && "$bin/golangci-lint" run "./${rel_dir#/}" 2>&1)"; then
  printf '%s\n' "$lint_output" >&2
  exit 2
fi
exit 0
