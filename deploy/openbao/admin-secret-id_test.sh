#!/bin/sh
# Tests for admin-secret-id.sh. Run with no arguments from anywhere; prints
# "ok" and exits 0 when every case passes. Docker is not used: a fake docker
# on PATH answers "volume inspect NAME" from a folder of volume folders.
set -u

here="$(cd "$(dirname "$0")" && pwd)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0

fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

volumes="$tmp/volumes"
mkdir -p "$tmp/path"
cat >"$tmp/path/docker" <<EOF
#!/bin/sh
if [ "\$1" = volume ] && [ "\$2" = inspect ]; then
	test -d "$volumes/\$3"
	exit
fi
exit 125
EOF
chmod 755 "$tmp/path/docker"

home="$tmp/home"
file="$home/.config/secret-externalizer/openbao-admin.secret-id"

# reset: removes the home folder and every volume.
reset() {
	rm -rf "$home" "$volumes"
	mkdir -p "$home" "$volumes"
}

# run_script [VAR=value...]: runs the script and sets status and output.
run_script() {
	output=$(env HOME="$home" PATH="$tmp/path:$PATH" "$@" sh "$here/admin-secret-id.sh" 2>&1)
	status=$?
}

mode() {
	ls -ld "$1" | cut -c 1-10
}

# A new secret-id on a machine with no volumes: 64 hexadecimal digits and
# no newline.
reset
run_script
[ "$status" -eq 0 ] || fail "new secret-id: exit $status: $output"
[ "$(wc -c <"$file" | tr -d ' ')" -eq 64 ] || fail "new secret-id: $file does not hold 64 bytes"
LC_ALL=C grep -Eqx '[0-9a-f]{64}' "$file" || fail "new secret-id: $file is not 64 hexadecimal digits"
[ "$(mode "$file")" = "-rw-r--r--" ] || fail "new secret-id: mode $(mode "$file"), want -rw-r--r--"
[ "$(mode "$(dirname "$file")")" = "drwx------" ] || fail "new secret-id: folder mode $(mode "$(dirname "$file")"), want drwx------"
case $output in
*"$(cat "$file")"*) fail "new secret-id: the output shows the secret-id" ;;
esac
first=$(cat "$file")

# Running again leaves it alone, also once the volumes exist.
mkdir -p "$volumes/secret-externalizer_openbao-data"
run_script
[ "$status" -eq 0 ] || fail "existing secret-id: exit $status: $output"
[ "$(cat "$file")" = "$first" ] || fail "existing secret-id: the secret-id changed"

# A file that is not 64 hexadecimal digits is refused.
for bad in short "$(printf '%064d' 0)
" "$(printf 'G%063d' 0)"; do
	reset
	mkdir -p -m 0700 "$(dirname "$file")"
	printf '%s' "$bad" >"$file"
	run_script
	[ "$status" -ne 0 ] || fail "bad secret-id '$bad': exit 0, want a failure"
done

# A folder that others may enter is refused.
reset
mkdir -p "$(dirname "$file")"
chmod 0755 "$(dirname "$file")"
run_script
[ "$status" -ne 0 ] || fail "open folder: exit 0, want a failure"
[ ! -e "$file" ] || fail "open folder: a secret-id was written"

# A relative path is refused.
reset
run_script OPENBAO_ADMIN_SECRET_ID_FILE=relative/secret-id
[ "$status" -ne 0 ] || fail "relative path: exit 0, want a failure"

# A data volume without the file is refused instead of getting a new
# secret-id that its admin role would not know.
reset
mkdir -p "$volumes/secret-externalizer_openbao-data"
run_script
[ "$status" -ne 0 ] || fail "existing storage: exit 0, want a failure"
[ ! -e "$file" ] || fail "existing storage: a secret-id was written"

# OPENBAO_ADMIN_SECRET_ID_FILE chooses the file.
reset
run_script OPENBAO_ADMIN_SECRET_ID_FILE="$home/keys/admin.secret-id"
[ "$status" -eq 0 ] || fail "chosen file: exit $status: $output"
[ -f "$home/keys/admin.secret-id" ] || fail "chosen file: $home/keys/admin.secret-id was not written"

if [ "$failures" -ne 0 ]; then
	exit 1
fi
echo ok
