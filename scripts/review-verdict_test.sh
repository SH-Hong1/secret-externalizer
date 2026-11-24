#!/bin/sh
# Tests scripts/review-verdict.sh. It prints one line per failure and exits 1
# if any case fails.
set -eu

dir=$(cd "$(dirname "$0")" && pwd)
verdict="$dir/review-verdict.sh"
work=$(mktemp -d)
trap 'rm -r -f -- "$work"' EXIT
failed=0

# check <name> <override> <want status> <want text, or "" for empty output> <verdict JSON>
check() {
	name=$1 override=$2 want=$3 text=$4
	printf '%s' "$5" >"$work/verdict.json"
	if sh "$verdict" "$work/verdict.json" "$override" >"$work/out" 2>"$work/err"; then got=0; else got=$?; fi
	if [ "$got" != "$want" ]; then
		printf 'review-verdict: %s: status %s, want %s; output: %s %s\n' "$name" "$got" "$want" "$(cat "$work/out")" "$(cat "$work/err")" >&2
		failed=1
		return
	fi
	if [ -z "$text" ]; then
		if [ -s "$work/out" ]; then
			printf 'review-verdict: %s: output %s, want none\n' "$name" "$(cat "$work/out")" >&2
			failed=1
		fi
	elif ! grep -qF -- "$text" "$work/out"; then
		printf 'review-verdict: %s: output does not contain %s: %s\n' "$name" "$text" "$(cat "$work/out")" >&2
		failed=1
	fi
}

errors='{"file":"a.go","line":3,"section":"Errors","fix":"wrap it"}'
security='{"file":"b.go","line":9,"section":"Security","fix":"do not log the token"}'
no_verdict='검토자가 판정을 내지 못했습니다'

check 'no violations' false 0 '' '{"violations":[]}'
check 'violation' false 1 'a.go:3 — Errors — wrap it' "{\"violations\":[$errors]}"
check 'violation with override' true 0 'a.go:3 — Errors — wrap it' "{\"violations\":[$errors]}"
check 'override is mentioned' true 0 'review: override' "{\"violations\":[$errors]}"
check 'security with override' true 1 'b.go:9 — Security — do not log the token' "{\"violations\":[$errors,$security]}"
check 'empty' false 2 "$no_verdict" ''
check 'not json' false 2 "$no_verdict" 'not json'
check 'no violations key' false 2 "$no_verdict" '{}'
check 'violations not a list' true 2 "$no_verdict" '{"violations":"x"}'

eleven=$errors
i=1
while [ "$i" -lt 11 ]; do
	eleven="$eleven,$errors"
	i=$((i + 1))
done
printf '{"violations":[%s]}' "$eleven" >"$work/verdict.json"
sh "$verdict" "$work/verdict.json" false >"$work/out" || true
lines=$(grep -c '^- ' "$work/out" || true)
if [ "$lines" != 10 ]; then
	printf 'review-verdict: eleven violations: %s listed, want 10\n' "$lines" >&2
	failed=1
fi

exit "$failed"
