#!/bin/sh
# Tests for unseal-key.sh. Run with no arguments from anywhere; prints "ok"
# and exits 0 when every case passes. Docker is not used: a fake docker on
# PATH keeps each named volume as a folder and runs the script given to
# `sh -c` on the host, with the container paths replaced by those folders.
set -u

here="$(cd "$(dirname "$0")" && pwd)"
tmp=$(mktemp -d)
trap 'chmod -R u+rwx "$tmp" 2>/dev/null; rm -rf "$tmp"' EXIT
failures=0

fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

volumes="$tmp/volumes"
mkdir -p "$tmp/path"
cat >"$tmp/path/docker" <<EOF
#!/bin/sh
# Understands "volume inspect NAME" and
# "run [options] --volume NAME:/secrets[:ro] [--volume FILE:/host-key:ro] ... -c SCRIPT".
if [ "\$1" = volume ] && [ "\$2" = inspect ]; then
	test -d "$volumes/\$3"
	exit
fi
[ "\$1" = run ] || exit 125
secrets= hostkey= script=
while [ \$# -gt 0 ]; do
	case \$1 in
	--volume)
		case \$2 in
		*:/secrets | *:/secrets:ro) secrets="$volumes/\${2%%:*}" ;;
		*:/host-key:ro) hostkey=\${2%%:*} ;;
		esac
		shift
		;;
	-c) script=\$2; shift ;;
	esac
	shift
done
[ -d "\$secrets" ] || exit 125
script=\$(printf '%s' "\$script" | sed -e "s#/secrets#\$secrets#g" -e "s#/host-key#\$hostkey#g")
exec sh -c "\$script"
EOF
chmod 755 "$tmp/path/docker"

home="$tmp/home"
key="$home/.config/secret-externalizer/openbao-unseal.key"

# reset: removes the home folder and every volume.
reset() {
	chmod -R u+rwx "$tmp/home" "$volumes" 2>/dev/null
	rm -rf "$tmp/home" "$volumes"
	mkdir -p "$home" "$volumes"
}

# run_script [VAR=value...]: runs the script and sets status and output.
run_script() {
	output=$(env HOME="$home" PATH="$tmp/path:$PATH" "$@" sh "$here/unseal-key.sh" 2>&1)
	status=$?
}

# volume_with_key NAME: makes a secrets volume whose unseal/key holds 32
# known bytes, and writes the same bytes to $tmp/old-key.
volume_with_key() {
	mkdir -p "$volumes/secret-externalizer_openbao-secrets/unseal"
	printf 'dev-only-old-unseal-key-32bytes!' >"$tmp/old-key"
	cp "$tmp/old-key" "$volumes/secret-externalizer_openbao-secrets/unseal/key"
}

mode() {
	ls -ld "$1" | cut -c 1-10
}

# A new key on a machine with no volumes.
reset
run_script
[ "$status" -eq 0 ] || fail "new key: exit $status: $output"
[ "$(wc -c <"$key" | tr -d ' ')" -eq 32 ] || fail "new key: $key does not hold 32 bytes"
[ "$(mode "$key")" = "-rw-r--r--" ] || fail "new key: mode $(mode "$key"), want -rw-r--r--"
[ "$(mode "$(dirname "$key")")" = "drwx------" ] || fail "new key: folder mode $(mode "$(dirname "$key")"), want drwx------"
first=$(od -An -tx1 "$key")

# Running again leaves the key alone.
run_script
[ "$status" -eq 0 ] || fail "existing key: exit $status: $output"
[ "$(od -An -tx1 "$key")" = "$first" ] || fail "existing key: the key changed"

# A key of the wrong size is refused.
reset
mkdir -p -m 0700 "$(dirname "$key")"
printf 'short' >"$key"
run_script
[ "$status" -ne 0 ] || fail "short key: exit 0, want a failure"

# A folder that others may enter is refused.
reset
mkdir -p "$(dirname "$key")"
chmod 0755 "$(dirname "$key")"
run_script
[ "$status" -ne 0 ] || fail "open folder: exit 0, want a failure"
[ ! -e "$key" ] || fail "open folder: a key was written"

# A relative path is refused.
reset
run_script OPENBAO_UNSEAL_KEY_FILE=relative/key
[ "$status" -ne 0 ] || fail "relative path: exit 0, want a failure"

# The volume's key moves to the file and leaves the volume.
reset
volume_with_key
run_script
[ "$status" -eq 0 ] || fail "move: exit $status: $output"
cmp -s "$key" "$tmp/old-key" || fail "move: $key does not hold the volume's key"
[ ! -e "$volumes/secret-externalizer_openbao-secrets/unseal" ] || fail "move: the volume still has unseal/"

# A volume left with an empty unseal folder is cleaned up on the next run.
mkdir -p "$volumes/secret-externalizer_openbao-secrets/unseal"
run_script
[ "$status" -eq 0 ] || fail "empty folder: exit $status: $output"
[ ! -e "$volumes/secret-externalizer_openbao-secrets/unseal" ] || fail "empty folder: the volume still has unseal/"

# A file whose key differs from the volume's leaves both alone and fails.
reset
volume_with_key
mkdir -p -m 0700 "$(dirname "$key")"
printf 'dev-only-other-unseal-key-32byte' >"$key"
run_script
[ "$status" -ne 0 ] || fail "different key: exit 0, want a failure"
cmp -s "$volumes/secret-externalizer_openbao-secrets/unseal/key" "$tmp/old-key" || fail "different key: the volume's key changed"

# A data volume without any key is refused instead of getting a new key.
reset
mkdir -p "$volumes/secret-externalizer_openbao-data" "$volumes/secret-externalizer_openbao-secrets/worker"
run_script
[ "$status" -ne 0 ] || fail "lost key: exit 0, want a failure"
[ ! -e "$key" ] || fail "lost key: a key was written"

# OPENBAO_UNSEAL_KEY_FILE chooses the file.
reset
run_script OPENBAO_UNSEAL_KEY_FILE="$tmp/home/keys/bao.key"
[ "$status" -eq 0 ] || fail "chosen file: exit $status: $output"
[ -f "$tmp/home/keys/bao.key" ] || fail "chosen file: $tmp/home/keys/bao.key was not written"

if [ "$failures" -ne 0 ]; then
	exit 1
fi
echo ok
