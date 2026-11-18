#!/usr/bin/env bash
# Tests for the hook scripts in this directory. Run with no arguments from
# anywhere inside the repository. Prints "ok" and exits 0 when every case
# passes.
#
# The cases run against a throwaway copy of the module configuration in a
# temporary directory, so nothing is written inside the repository.
set -u

HOOKS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HOOKS/../.." && pwd)"
TESTDATA="$HOOKS/testdata"
TMP="$(mktemp -d)"
SANDBOX="$TMP/repo"
PKG="$SANDBOX/internal/hooktest"
TMP_OUTSIDE="$TMP/outside"
NO_JQ_PATH="$TMP/nojq"
failures=0
status=0
output=""
stdout=""

trap 'rm -rf "$TMP"' EXIT

# A PATH that has what the hooks need except jq.
mkdir -p "$NO_JQ_PATH"
for tool in bash sh cat tail dirname basename git make go; do
  ln -s "$(command -v "$tool")" "$NO_JQ_PATH/$tool"
done

assert_eq() {
  if [ "$1" != "$2" ]; then
    printf 'FAIL %s: %s: want %q, got %q\n' "$current" "$3" "$1" "$2"
    failures=$((failures + 1))
  fi
}

assert_contains() {
  case "$1" in
    *"$2"*) ;;
    *)
      printf 'FAIL %s: output does not contain %q; output was:\n%s\n' "$current" "$2" "$1"
      failures=$((failures + 1))
      ;;
  esac
}

# reset_sandbox recreates the throwaway module with one committed, clean tree.
reset_sandbox() {
  rm -rf "$SANDBOX" "$TMP_OUTSIDE"
  mkdir -p "$PKG" "$TMP_OUTSIDE"
  cp "$ROOT/go.mod" "$ROOT/.golangci.yml" "$SANDBOX/"
  git -C "$SANDBOX" init -q
  git -C "$SANDBOX" add -A
  git -C "$SANDBOX" -c user.name=test -c user.email=test@example.invalid commit -q -m init
}

# run_hook SCRIPT INPUT runs a hook script with INPUT on stdin, as Claude Code
# would with the sandbox as the project. It sets $status to the exit code,
# $output to stderr, and $stdout to stdout. HOOK_PATH overrides PATH for the
# hook process.
run_hook() {
  local errfile="$TMP/stderr"
  stdout="$(printf '%s' "$2" | CLAUDE_PROJECT_DIR="$SANDBOX" \
    HOOK_BIN_DIR="${HOOK_BIN_DIR:-$ROOT/bin}" \
    PATH="${HOOK_PATH:-$PATH}" \
    /bin/bash "$HOOKS/$1" 2>"$errfile")"
  status=$?
  output="$(cat "$errfile")"
}

# run_edit_hook FILE_PATH runs go-edit-check.sh for an edit to FILE_PATH.
run_edit_hook() {
  run_hook go-edit-check.sh "$(jq -n --arg p "$1" '{tool_name: "Edit", tool_input: {file_path: $p}}')"
}

# run_stop_hook ACTIVE runs stop-verify.sh. ACTIVE is the value of
# stop_hook_active.
run_stop_hook() {
  run_hook stop-verify.sh "$(jq -n --argjson a "$1" '{hook_event_name: "Stop", stop_hook_active: $a}')"
}

case_unformatted_go_fails() {
  cp "$TESTDATA/unformatted.go.txt" "$PKG/sample.go"
  run_edit_hook "$PKG/sample.go"
  assert_eq 2 "$status" "unformatted file"
  assert_contains "$output" "gofumpt"
}

case_lint_violation_fails() {
  cp "$TESTDATA/errcheck.go.txt" "$PKG/sample.go"
  run_edit_hook "$PKG/sample.go"
  assert_eq 2 "$status" "errcheck violation"
  assert_contains "$output" "errcheck"
}

case_clean_go_passes() {
  cp "$TESTDATA/clean.go.txt" "$PKG/sample.go"
  run_edit_hook "$PKG/sample.go"
  assert_eq 0 "$status" "clean file"
  assert_eq "" "$output" "clean file output"
}

case_non_go_file_passes_silently() {
  echo "# notes" >"$SANDBOX/NOTES.md"
  run_edit_hook "$SANDBOX/NOTES.md"
  assert_eq 0 "$status" "markdown file"
  assert_eq "" "$output" "markdown file output"
}

case_path_with_space_and_hangul() {
  mkdir -p "$PKG/한글 폴더"
  cp "$TESTDATA/unformatted.go.txt" "$PKG/한글 폴더/sample.go"
  run_edit_hook "$PKG/한글 폴더/sample.go"
  assert_eq 2 "$status" "path with space and hangul"
  assert_contains "$output" "gofumpt: internal/hooktest/한글 폴더/sample.go"
}

case_file_outside_repo_passes() {
  cp "$TESTDATA/unformatted.go.txt" "$TMP_OUTSIDE/sample.go"
  run_edit_hook "$TMP_OUTSIDE/sample.go"
  assert_eq 0 "$status" "file outside repository"
}

case_testdata_file_passes() {
  mkdir -p "$PKG/testdata"
  cp "$TESTDATA/unformatted.go.txt" "$PKG/testdata/sample.go"
  run_edit_hook "$PKG/testdata/sample.go"
  assert_eq 0 "$status" "file under testdata"
}

case_missing_tools_fails_loudly() {
  cp "$TESTDATA/clean.go.txt" "$PKG/sample.go"
  HOOK_BIN_DIR="$TMP_OUTSIDE/empty" run_edit_hook "$PKG/sample.go"
  assert_eq 2 "$status" "missing tools"
  assert_contains "$output" "make tools"
}

case_edit_hook_without_jq_fails_loudly() {
  cp "$TESTDATA/unformatted.go.txt" "$PKG/sample.go"
  HOOK_PATH="$NO_JQ_PATH" run_edit_hook "$PKG/sample.go"
  assert_eq 2 "$status" "jq missing"
  assert_contains "$output" "jq is required"
}

case_deleted_file_passes() {
  run_edit_hook "$PKG/gone.go"
  assert_eq 0 "$status" "file that no longer exists"
}

case_stop_passes_when_check_passes() {
  HOOK_CHECK_CMD="true" run_stop_hook false
  assert_eq 0 "$status" "check passes"
}

case_stop_blocks_when_check_fails() {
  HOOK_CHECK_CMD="sh -c 'echo lint-broke; exit 1'" run_stop_hook false
  assert_eq 2 "$status" "check fails"
  assert_contains "$output" "lint-broke"
}

case_stop_blocks_when_work_is_already_committed() {
  cp "$TESTDATA/errcheck.go.txt" "$PKG/sample.go"
  git -C "$SANDBOX" add -A
  git -C "$SANDBOX" -c user.name=test -c user.email=test@example.invalid commit -q -m work
  HOOK_CHECK_CMD="sh -c 'exit 1'" run_stop_hook false
  assert_eq 2 "$status" "clean tree, failing check"
}

case_stop_blocks_when_go_file_is_in_new_directory() {
  mkdir -p "$SANDBOX/internal/newpkg"
  cp "$TESTDATA/errcheck.go.txt" "$SANDBOX/internal/newpkg/sample.go"
  HOOK_CHECK_CMD="sh -c 'exit 1'" run_stop_hook false
  assert_eq 2 "$status" "untracked directory, failing check"
}

case_stop_does_not_loop() {
  HOOK_CHECK_CMD="sh -c 'echo lint-broke; exit 1'" run_stop_hook true
  assert_eq 0 "$status" "second stop after a block"
  assert_contains "$stdout" '"systemMessage"'
  assert_contains "$stdout" "still failing"
  assert_contains "$stdout" "lint-broke"
}

case_stop_without_jq_reports_and_does_not_loop() {
  HOOK_PATH="$NO_JQ_PATH" HOOK_CHECK_CMD="sh -c 'exit 1'" run_stop_hook true
  assert_eq 0 "$status" "jq missing"
  assert_contains "$output" "jq is required"
}

for current in $(declare -F | awk '{print $3}' | grep '^case_'); do
  reset_sandbox
  "$current"
done

if [ "$failures" -ne 0 ]; then
  printf '%d assertion(s) failed\n' "$failures"
  exit 1
fi
echo ok
