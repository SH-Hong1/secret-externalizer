#!/bin/sh
# Tests scripts/commit-lint.sh. It prints one line per failure and exits 1 if
# any case fails.
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
lint="$dir/commit-lint.sh"
work=$(mktemp -d)
trap 'rm -r -f -- "$work"' EXIT
failed=0

# check <accept|reject> <name> <message>
check() {
	want=$1
	name=$2
	printf '%s\n' "$3" >"$work/msg"
	if sh "$lint" "$work/msg" 2>/dev/null; then got=accept; else got=reject; fi
	if [ "$got" != "$want" ]; then
		printf 'commit-lint: %s: got %s, want %s\n' "$name" "$got" "$want" >&2
		failed=1
	fi
}

# check_title <accept|reject> <title>
check_title() {
	if sh "$lint" --title "$2" 2>/dev/null; then got=accept; else got=reject; fi
	if [ "$got" != "$1" ]; then
		printf 'commit-lint --title %s: got %s, want %s\n' "$2" "$got" "$1" >&2
		failed=1
	fi
}

# check_closes <accept|reject> <body>
check_closes() {
	printf '%s\n' "$2" >"$work/body"
	if sh "$lint" --closes "$work/body" 2>/dev/null; then got=accept; else got=reject; fi
	if [ "$got" != "$1" ]; then
		printf 'commit-lint --closes %s: got %s, want %s\n' "$2" "$got" "$1" >&2
		failed=1
	fi
}

nl='
'
check accept 'type and issue' "feat: add the version command${nl}${nl}Refs #3"
check accept 'scope' "fix(fact): keep the evidence order stable${nl}${nl}Refs #12"
check accept 'body' "docs: describe the bundle${nl}${nl}The summary file was missing.${nl}${nl}Refs #7"
check accept 'breaking' "feat(cli)!: rename the out flag${nl}${nl}BREAKING CHANGE: --out is now --output.${nl}${nl}Refs #9"
check accept 'closes' "build: pin the linter${nl}${nl}Closes #1"
check accept 'trailer after the issue' "test: cover the redactor${nl}${nl}Refs #5${nl}${nl}Co-Authored-By: Someone <someone@example.invalid>"
check accept 'comment lines' "ci: run the checks${nl}# a comment${nl}${nl}Refs #2"
check accept 'merge' "Merge branch 'main' into feat/3-version"
check accept 'fixup' 'fixup! feat: add the version command'
check reject 'unknown type' "chore: tidy${nl}${nl}Refs #3"
check reject 'no type' "Add the version command${nl}${nl}Refs #3"
check reject 'no space after the colon' "feat:add the version command${nl}${nl}Refs #3"
check reject 'upper-case scope' "feat(Fact): add evidence${nl}${nl}Refs #3"
check reject 'upper-case subject' "feat: Add the version command${nl}${nl}Refs #3"
check reject 'full stop' "feat: add the version command.${nl}${nl}Refs #3"
check reject 'long first line' "feat: add a version command whose description is far longer than it should be${nl}${nl}Refs #3"
check reject 'no blank line' "feat: add the version command${nl}Refs #3"
check reject 'no issue' 'feat: add the version command'
check reject 'issue only in the first line' 'feat: add the version command #3'
check reject 'empty' ''

check_title accept 'feat(fact): add the evidence type'
check_title reject 'Add the evidence type'
check_title reject 'feat: Add the evidence type'

check_closes accept "설명${nl}${nl}Closes #12"
check_closes accept 'fixes #4'
check_closes reject "설명${nl}${nl}Refs #12"
check_closes reject '설명'

check accept 'scissors' "docs: fix a link${nl}${nl}Refs #4${nl}# ------------------------ >8 ------------------------${nl}diff --git a/x b/x"
check_title reject '기능 추가'

# The pre-commit cases run the hook in a throwaway repository whose make and
# bin/golangci-lint are fakes that record their calls.
repo="$work/repo"
git init -q "$repo"
git -C "$repo" config user.email test@example.invalid
git -C "$repo" config user.name test
mkdir -p "$repo/.githooks" "$repo/bin" "$repo/fake"
cp "$dir/../.githooks/pre-commit" "$repo/.githooks/pre-commit"
printf '#!/bin/sh\necho "make $*" >>"%s/calls"\n' "$work" >"$repo/fake/make"
printf '#!/bin/sh\necho "golangci-lint $*" >>"%s/calls"\n' "$work" >"$repo/bin/golangci-lint"
chmod +x "$repo/fake/make" "$repo/bin/golangci-lint"
printf 'bin/\nfake/\n' >"$repo/.gitignore"
git -C "$repo" add .gitignore .githooks
git -C "$repo" commit -q -m init --no-verify

# precommit runs the hook in the sandbox and sets $status.
precommit() {
	: >"$work/calls"
	if (cd "$repo" && PATH="$repo/fake:$PATH" sh .githooks/pre-commit) 2>"$work/err"; then status=0; else status=$?; fi
}

printf '# x\n' >"$repo/README.md"
git -C "$repo" add README.md
precommit
if [ "$status" -ne 0 ] || [ -s "$work/calls" ]; then
	printf 'pre-commit: docs only: status %s, calls %s, want 0 and none\n' "$status" "$(cat "$work/calls")" >&2
	failed=1
fi
git -C "$repo" commit -q -m docs --no-verify

printf 'package a\n' >"$repo/a.go"
git -C "$repo" add a.go
precommit
if [ "$status" -ne 0 ] || ! grep -q '^make -s check$' "$work/calls"; then
	printf 'pre-commit: go file: status %s, calls %s, want 0 and make -s check\n' "$status" "$(cat "$work/calls")" >&2
	failed=1
fi

printf 'package a\n\nvar X = 1\n' >"$repo/a.go"
precommit
if [ "$status" -eq 0 ] || ! grep -q 'has changes that are not staged' "$work/err"; then
	printf 'pre-commit: partly staged go file: status %s, want non-zero and the unstaged message\n' "$status" >&2
	failed=1
fi

exit "$failed"
