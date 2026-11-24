#!/bin/sh
# Makes the file that holds the secret-id of OpenBao's admin AppRole,
# outside every Docker volume. Run it on the host, once, before the first
# `docker compose up`; `make openbao-host-files` runs it.
#
# The file is OPENBAO_ADMIN_SECRET_ID_FILE, or
# $HOME/.config/secret-externalizer/openbao-admin.secret-id when that is
# unset, the same default compose.yaml uses. It holds 64 hexadecimal digits
# and no newline.
#
# The admin role may change every policy and every role but its own, so it
# can grant any permission; see decision 0069. Self-initialisation in
# config.hcl registers this secret-id for it on the first start, and the
# openbao-roles service logs in with it on every start to bring the roles
# and policies up to date. No other service sees the file.
#
# - When the file exists, it is checked and not changed.
# - When it does not and the openbao-data volume exists, it stops: that
#   storage's admin role was made with another secret-id, or without one,
#   and a new secret-id would not log in.
# - Otherwise a new secret-id is made.
#
# The folder of the file is made with mode 0700 and must not let anyone but
# its owner enter it. The file itself is 0644 so that OpenBao and the
# openbao-roles service, which run as uid 100 in their containers, can read
# it through the bind mount on a Linux host; the folder keeps other users of
# the host out.
set -eu

file=${OPENBAO_ADMIN_SECRET_ID_FILE:-${HOME:?HOME is not set}/.config/secret-externalizer/openbao-admin.secret-id}
data_volume=secret-externalizer_openbao-data
docker=${DOCKER:-docker}

case $file in
/*) ;;
*)
	echo "OPENBAO_ADMIN_SECRET_ID_FILE must be an absolute path, not '$file'" >&2
	exit 1
	;;
esac
dir=$(dirname "$file")

# check_content FILE: fails unless FILE holds exactly 64 hexadecimal digits.
check_content() {
	size=$(wc -c <"$1" | tr -d ' ')
	if [ "$size" -ne 64 ] || ! LC_ALL=C grep -Eqx '[0-9a-f]{64}' "$1"; then
		echo "$1 does not hold the 64 hexadecimal digits of a secret-id, with no newline" >&2
		exit 1
	fi
}

# check_dir: fails when someone other than the owner may enter the folder.
check_dir() {
	if [ -n "$(find "$dir" -maxdepth 0 -perm -0010)" ] || [ -n "$(find "$dir" -maxdepth 0 -perm -0001)" ]; then
		echo "$dir lets other users enter it; make it 0700 (chmod 0700 '$dir') or choose another OPENBAO_ADMIN_SECRET_ID_FILE" >&2
		exit 1
	fi
}

if [ -e "$file" ]; then
	check_dir
	check_content "$file"
	echo "the admin secret-id exists at $file"
	exit 0
fi

if "$docker" volume inspect "$data_volume" >/dev/null 2>&1; then
	echo "the volume $data_volume exists but $file does not;" >&2
	echo "a new secret-id cannot log in as that storage's admin role. Put the secret-id it was made with at $file," >&2
	echo "or remove the volumes openbao-data and openbao-secrets to start empty (this deletes every value OpenBao holds); see docs/local-run.md." >&2
	exit 1
fi

if [ ! -d "$dir" ]; then
	mkdir -p -m 0700 "$dir"
fi
check_dir
tmp="$file.tmp.$$"
trap 'rm -f "$tmp"' EXIT
umask 022
od -An -tx1 -N32 /dev/urandom | tr -d ' \n' >"$tmp"
check_content "$tmp"
chmod 0644 "$tmp"
mv "$tmp" "$file"
trap - EXIT
echo "created a new admin secret-id at $file"
