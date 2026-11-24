#!/bin/sh
# Fetches the public sample projects pinned in a lock file.
#
#   fetch-sample.sh <lock-file> <cache-dir>
#
# Each line of <lock-file> is "<name> <git URL> <commit>"; blank lines and
# lines starting with # are skipped. Each sample is checked out at its commit
# in <cache-dir>/<name>-<commit>, and that path is printed on its own line.
# A copy already at the commit is used as it is; any other copy is replaced.
# The samples are kept outside the repository and never committed.
set -eu

[ $# -eq 2 ] && [ -n "$1" ] && [ -n "$2" ] || {
	echo 'usage: fetch-sample.sh <lock-file> <cache-dir>' >&2
	exit 1
}
lock=$1
cache=$2

grep -v -e '^#' -e '^[[:space:]]*$' "$lock" | while read -r name url commit extra; do
	if [ -z "$commit" ] || [ -n "$extra" ]; then
		printf 'fetch-sample: %s: want "<name> <git URL> <commit>"\n' "$lock" >&2
		exit 1
	fi
	dest="$cache/$name-$commit"
	if [ -d "$dest/.git" ] && [ "$(git -C "$dest" rev-parse HEAD 2>/dev/null)" = "$commit" ] &&
		[ -z "$(git -C "$dest" status --porcelain)" ]; then
		printf '%s\n' "$dest"
		continue
	fi
	rm -r -f -- "$dest"
	mkdir -p "$dest"
	git -C "$dest" init -q
	if ! git -C "$dest" fetch -q --depth 1 "$url" "$commit" 2>/dev/null; then
		rm -r -f -- "$dest"
		printf 'fetch-sample: %s: commit %s not found at %s\n' "$name" "$commit" "$url" >&2
		exit 1
	fi
	git -C "$dest" -c advice.detachedHead=false checkout -q --detach FETCH_HEAD
	got=$(git -C "$dest" rev-parse HEAD)
	if [ "$got" != "$commit" ]; then
		printf 'fetch-sample: %s: fetched %s, want %s\n' "$name" "$got" "$commit" >&2
		exit 1
	fi
	printf '%s\n' "$dest"
done
