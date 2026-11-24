#!/usr/bin/env bash
# Tests that the linter configuration in .golangci.yml catches the rules
# AGENTS.md hands to it. Each case copies one Go file from
# scripts/testdata/lint-rules into a throwaway module and runs gofumpt and
# golangci-lint from bin/ on it. Prints "ok" and exits 0 when every case
# passes.
set -u

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
testdata="$root/scripts/testdata/lint-rules"
bin="$root/bin"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
failures=0

if [ ! -x "$bin/gofumpt" ] || [ ! -x "$bin/golangci-lint" ]; then
  echo "lint-rules_test: gofumpt or golangci-lint is missing in $bin; run 'make tools' first" >&2
  exit 1
fi

# lint FILE... copies each testdata file into a fresh package, named by
# dropping ".txt", and sets $status and $output from gofumpt and
# golangci-lint.
lint() {
  rm -rf "$tmp/m"
  mkdir -p "$tmp/m/internal/linttest"
  cp "$root/go.mod" "$root/.golangci.yml" "$tmp/m/"
  for f in "$@"; do
    cp "$testdata/$f.txt" "$tmp/m/internal/linttest/$f"
  done
  if unformatted="$("$bin/gofumpt" -extra -l "$tmp/m/internal/linttest")" && [ -n "$unformatted" ]; then
    status=1
    output="gofumpt: $unformatted"
    return
  fi
  if output="$(cd "$tmp/m" && "$bin/golangci-lint" run ./internal/linttest 2>&1)"; then status=0; else status=1; fi
}

# expect NAME WANT_STATUS WANT_TEXT FILE...
expect() {
  name=$1 want=$2 text=$3
  shift 3
  lint "$@"
  if [ "$status" != "$want" ]; then
    printf 'FAIL %s: status %s, want %s; output:\n%s\n' "$name" "$status" "$want" "$output"
    failures=$((failures + 1))
    return
  fi
  case "$output" in
    *"$text"*) ;;
    *)
      printf 'FAIL %s: output does not contain %q; output:\n%s\n' "$name" "$text" "$output"
      failures=$((failures + 1))
      ;;
  esac
}

expect clean 0 "" clean.go
expect unformatted 1 "gofumpt" unformatted.go
expect errcheck 1 "errcheck" errcheck.go
expect nolint-without-reason 1 "nolintlint" nolint_no_reason.go
expect slog-dynamic-message 1 "message should be a string literal or a constant" slog_dynamic_msg.go
expect slog-camel-case-key 1 "keys should be written in snake_case" slog_camel_key.go
expect test-in-same-package 1 "testpackage" clean.go samepkg_test.go
expect exec-command-context 0 "" exec.go

if [ "$failures" -ne 0 ]; then
  echo "$failures case(s) failed"
  exit 1
fi
echo ok
