#!/bin/sh
# Tests scripts/fetch-sample.sh against a repository in a temporary folder.
# It prints one line per failure and exits 1 if any case fails.
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
fetch="$dir/fetch-sample.sh"
work=$(mktemp -d)
trap 'rm -r -f -- "$work"' EXIT
failed=0

# The scripts in this file run git in repositories of their own.
unset GIT_DIR GIT_INDEX_FILE GIT_WORK_TREE GIT_OBJECT_DIRECTORY GIT_PREFIX

fail() {
	printf 'fetch-sample: %s\n' "$1" >&2
	failed=1
}

src="$work/src"
git init -q "$src"
git -C "$src" config user.email test@example.invalid
git -C "$src" config user.name test
printf 'a\n' >"$src/a.txt"
git -C "$src" add a.txt
git -C "$src" commit -q -m one
first=$(git -C "$src" rev-parse HEAD)
printf 'b\n' >"$src/a.txt"
git -C "$src" commit -q -am two
second=$(git -C "$src" rev-parse HEAD)
# A fetch by commit needs the server to allow it.
git -C "$src" config uploadpack.allowAnySHA1InWant true

cache="$work/cache"
printf '# a comment\n\nsample file://%s %s\n' "$src" "$first" >"$work/lock"

if ! out=$(sh "$fetch" "$work/lock" "$cache" 2>"$work/err"); then
	fail "pinned commit: failed: $(cat "$work/err")"
elif [ "$out" != "$cache/sample-$first" ]; then
	fail "pinned commit: printed $out, want $cache/sample-$first"
elif [ "$(git -C "$out" rev-parse HEAD)" != "$first" ]; then
	fail "pinned commit: HEAD is not $first"
elif [ "$(cat "$out/a.txt")" != a ]; then
	fail "pinned commit: a.txt is not the pinned content"
fi

# A second run finds the copy and does not fetch again.
mkdir -p "$work/fake"
real_git=$(command -v git)
printf '#!/bin/sh\ncase "$*" in *fetch*) echo fetched >>"%s/fetches";; esac\nexec "%s" "$@"\n' "$work" "$real_git" >"$work/fake/git"
chmod +x "$work/fake/git"
if ! PATH="$work/fake:$PATH" sh "$fetch" "$work/lock" "$cache" >/dev/null 2>"$work/err"; then
	fail "second run: failed: $(cat "$work/err")"
elif [ -s "$work/fetches" ]; then
	fail "second run: fetched again"
fi

# A copy at another commit is fetched anew.
git -C "$cache/sample-$first" checkout -q --detach "$first"
printf 'changed\n' >"$cache/sample-$first/a.txt"
git -C "$cache/sample-$first" commit -q -am changed
if ! sh "$fetch" "$work/lock" "$cache" >/dev/null 2>"$work/err"; then
	fail "moved copy: failed: $(cat "$work/err")"
elif [ "$(git -C "$cache/sample-$first" rev-parse HEAD)" != "$first" ]; then
	fail "moved copy: HEAD was not set back to $first"
fi

# A commit the repository does not have.
missing=0123456789012345678901234567890123456789
printf 'sample file://%s %s\n' "$src" "$missing" >"$work/lock"
if sh "$fetch" "$work/lock" "$cache" >/dev/null 2>"$work/err"; then
	fail "missing commit: succeeded"
elif ! grep -q "commit $missing not found" "$work/err"; then
	fail "missing commit: message $(cat "$work/err")"
fi

# A line without three fields.
printf 'sample file://%s\n' "$src" >"$work/lock"
if sh "$fetch" "$work/lock" "$cache" >/dev/null 2>"$work/err"; then
	fail "two fields: succeeded"
fi

exit "$failed"
