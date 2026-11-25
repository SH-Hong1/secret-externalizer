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
	# git's error goes to a file beside the copy so that it can be shown only
	# when the fetch fails. A failed fetch means a missing commit only if the
	# repository answers; otherwise the repository or the network is at fault.
	if ! git -C "$dest" fetch -q --depth 1 "$url" "$commit" 2>"$dest.err"; then
		if git ls-remote -q "$url" >/dev/null 2>&1; then
			printf 'fetch-sample: %s: commit %s not found at %s\n' "$name" "$commit" "$url" >&2
		else
			printf 'fetch-sample: %s: cannot reach %s:\n' "$name" "$url" >&2
			sed 's/^/	/' "$dest.err" >&2
		fi
		rm -r -f -- "$dest" "$dest.err"
		exit 1
	fi
	rm -f -- "$dest.err"
	git -C "$dest" -c advice.detachedHead=false checkout -q --detach FETCH_HEAD
	got=$(git -C "$dest" rev-parse HEAD)
	if [ "$got" != "$commit" ]; then
		printf 'fetch-sample: %s: fetched %s, want %s\n' "$name" "$got" "$commit" >&2
		exit 1
	fi
	printf '%s\n' "$dest"
done
