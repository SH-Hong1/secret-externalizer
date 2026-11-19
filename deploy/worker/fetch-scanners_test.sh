#!/bin/sh
# Tests for fetch-scanners.sh. Run with no arguments from anywhere; prints
# "ok" and exits 0 when every case passes. Nothing is downloaded: a fake curl
# on PATH serves files from a temporary directory, and the script runs from a
# copy next to a test lock file.
set -u

here="$(cd "$(dirname "$0")" && pwd)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0

fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	else
		shasum -a 256 "$1" | cut -d ' ' -f 1
	fi
}

# The files the fake curl serves, by the last part of the URL.
served="$tmp/served"
mkdir -p "$served/pkg"
printf '#!/bin/sh\necho dev-only-fake-betterleaks\n' >"$served/pkg/betterleaks"
tar -czf "$served/betterleaks_fake.tar.gz" -C "$served/pkg" betterleaks
printf '#!/bin/sh\necho dev-only-fake-opengrep\n' >"$served/opengrep_fake"
good_tar=$(sha256 "$served/betterleaks_fake.tar.gz")
good_bin=$(sha256 "$served/opengrep_fake")
bad=0000000000000000000000000000000000000000000000000000000000000000

# A curl that understands only "--output <file> <url>" and refuses anything
# that is not https, like the real call.
mkdir -p "$tmp/path"
cat >"$tmp/path/curl" <<EOF
#!/bin/sh
out=
while [ \$# -gt 1 ]; do
	if [ "\$1" = --output ]; then out=\$2; shift; fi
	shift
done
case \$1 in https://*) ;; *) exit 1 ;; esac
src="$served/\${1##*/}"
[ -f "\$src" ] || exit 22
cp "\$src" "\$out"
EOF
chmod 755 "$tmp/path/curl"

# run_fetch <lock lines> <os> <arch> <dest>: runs a copy of the script with a
# lock file holding the given lines, and sets status and output.
run_fetch() {
	rm -rf "$tmp/script"
	mkdir -p "$tmp/script"
	cp "$here/fetch-scanners.sh" "$tmp/script/"
	printf '# test lock\n%s\n' "$1" >"$tmp/script/scanners.lock"
	output=$(PATH="$tmp/path:$PATH" sh "$tmp/script/fetch-scanners.sh" "$2" "$3" "$4" 2>&1)
	status=$?
}

lock_good="betterleaks 1.0.0 linux arm64 $good_tar https://example.invalid/betterleaks_fake.tar.gz
opengrep    2.0.0 linux arm64 $good_bin https://example.invalid/opengrep_fake
opengrep    2.0.0 linux amd64 $good_bin https://example.invalid/opengrep_fake"

# Every checksum matches: both executables are installed and executable.
dest="$tmp/dest-good"
run_fetch "$lock_good" linux arm64 "$dest"
[ "$status" -eq 0 ] || fail "good lock: status $status, output: $output"
for tool in betterleaks opengrep; do
	[ -x "$dest/$tool" ] || fail "good lock: $dest/$tool is not an executable file"
done
[ "$("$dest/opengrep" 2>/dev/null)" = dev-only-fake-opengrep ] || fail "good lock: opengrep is not the served file"
[ "$("$dest/betterleaks" 2>/dev/null)" = dev-only-fake-betterleaks ] || fail "good lock: betterleaks is not the file from the archive"

# One checksum does not match: nothing is installed.
dest="$tmp/dest-bad"
run_fetch "$(printf '%s\n' "$lock_good" | sed "s/arm64 $good_bin/arm64 $bad/")" linux arm64 "$dest"
[ "$status" -ne 0 ] || fail "mismatch: status 0, want failure"
case $output in *"checksum mismatch for opengrep"*) ;; *) fail "mismatch: output $output, want a checksum mismatch for opengrep" ;; esac
[ ! -e "$dest/betterleaks" ] && [ ! -e "$dest/opengrep" ] || fail "mismatch: something was installed in $dest"

# The archive's checksum does not match: nothing is installed.
dest="$tmp/dest-bad-tar"
run_fetch "$(printf '%s\n' "$lock_good" | sed "s/$good_tar/$bad/")" linux arm64 "$dest"
[ "$status" -ne 0 ] || fail "archive mismatch: status 0, want failure"
[ ! -e "$dest/betterleaks" ] && [ ! -e "$dest/opengrep" ] || fail "archive mismatch: something was installed in $dest"

# A tool has no line for the platform: the other one is not installed either.
dest="$tmp/dest-missing"
run_fetch "$lock_good" linux amd64 "$dest"
[ "$status" -ne 0 ] || fail "missing platform: status 0, want failure"
case $output in *"no betterleaks for linux/amd64"*) ;; *) fail "missing platform: output $output, want no betterleaks for linux/amd64" ;; esac
[ ! -e "$dest/opengrep" ] || fail "missing platform: opengrep was installed"

# The download fails.
dest="$tmp/dest-404"
run_fetch "$(printf '%s\n' "$lock_good" | sed 's/opengrep_fake/opengrep_missing/')" linux arm64 "$dest"
[ "$status" -ne 0 ] || fail "failed download: status 0, want failure"
[ ! -e "$dest/betterleaks" ] || fail "failed download: betterleaks was installed"

# A plain http URL is refused.
dest="$tmp/dest-http"
run_fetch "$(printf '%s\n' "$lock_good" | sed 's#https://#http://#')" linux arm64 "$dest"
[ "$status" -ne 0 ] || fail "http URL: status 0, want failure"

# Wrong number of arguments.
output=$(sh "$here/fetch-scanners.sh" linux 2>&1)
[ "$?" -eq 2 ] || fail "usage: want status 2, output: $output"

if [ "$failures" -ne 0 ]; then
	echo "$failures failure(s)" >&2
	exit 1
fi
echo ok
