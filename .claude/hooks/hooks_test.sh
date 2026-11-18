#!/usr/bin/env bash
# Tests for the hook scripts in this directory. Run with no arguments from
# anywhere inside the repository. Prints "ok" and exits 0 when every case
# passes.
set -u

HOOKS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HOOKS/../.." && pwd)"
TESTDATA="$HOOKS/testdata"
PKG="$ROOT/internal/hooktest_$$"
TMP_OUTSIDE="$(mktemp -d)"
failures=0
status=0
output=""

cleanup() {
  rm -rf "$PKG" "$TMP_OUTSIDE"
  rmdir "$ROOT/internal" 2>/dev/null || true
}
trap cleanup EXIT

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

# run_edit_hook FILE_PATH runs go-edit-check.sh as Claude Code would after an
# edit to FILE_PATH. It sets $status to the exit code and $output to stderr.
run_edit_hook() {
  local input
  input="$(jq -n --arg p "$1" '{tool_name: "Edit", tool_input: {file_path: $p}}')"
  output="$(printf '%s' "$input" | CLAUDE_PROJECT_DIR="$ROOT" bash "$HOOKS/go-edit-check.sh" 2>&1 >/dev/null)"
  status=$?
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
  run_edit_hook "$ROOT/AGENTS.md"
  assert_eq 0 "$status" "markdown file"
  assert_eq "" "$output" "markdown file output"
}

case_path_with_space_and_hangul() {
  mkdir -p "$PKG/한글 폴더"
  cp "$TESTDATA/unformatted.go.txt" "$PKG/한글 폴더/sample.go"
  run_edit_hook "$PKG/한글 폴더/sample.go"
  assert_eq 2 "$status" "path with space and hangul"
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

case_deleted_file_passes() {
  run_edit_hook "$PKG/gone.go"
  assert_eq 0 "$status" "file that no longer exists"
}

# run_stop_hook ACTIVE runs stop-verify.sh as Claude Code would when a turn
# ends. ACTIVE is the value of stop_hook_active. It sets $status and $output
# like run_edit_hook. The changed-file list defaults to one Go file so the
# check command runs unless a case overrides HOOK_CHANGED_FILES.
run_stop_hook() {
  local input
  input="$(jq -n --argjson a "$1" '{hook_event_name: "Stop", stop_hook_active: $a}')"
  output="$(printf '%s' "$input" | CLAUDE_PROJECT_DIR="$ROOT" \
    HOOK_CHANGED_FILES="${HOOK_CHANGED_FILES:-cmd/web/main.go}" \
    bash "$HOOKS/stop-verify.sh" 2>&1 >/dev/null)"
  status=$?
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

case_stop_does_not_loop() {
  HOOK_CHECK_CMD="sh -c 'exit 1'" run_stop_hook true
  assert_eq 0 "$status" "second stop after a block"
  assert_contains "$output" "still failing"
}

case_stop_skips_when_no_go_module_changes() {
  HOOK_CHECK_CMD="sh -c 'exit 1'" HOOK_CHANGED_FILES="docs/a.md" run_stop_hook false
  assert_eq 0 "$status" "only documents changed"
}

for current in $(declare -F | awk '{print $3}' | grep '^case_'); do
  rm -rf "$PKG"
  mkdir -p "$PKG"
  "$current"
done

if [ "$failures" -ne 0 ]; then
  printf '%d assertion(s) failed\n' "$failures"
  exit 1
fi
echo ok
