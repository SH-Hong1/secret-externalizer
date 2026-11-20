#!/bin/sh
# Creates the OpenBao unseal key and the AppRole secret-ids in the
# openbao-secrets volume, mounted at /secrets. Each file is created only when
# it is missing, so running this again never replaces a key that OpenBao
# already uses.
#
# Layout and owners:
#   /secrets/unseal/key         32 random bytes  openbao (100:1000), 0400
#   /secrets/worker/secret-id   64 hex digits    $SE_UID:1000, 0440
#   /secrets/web/secret-id      64 hex digits    65532:1000, 0440
#
# OpenBao (uid 100, gid 1000) reads every file. The worker and web containers
# mount only their own directory, so neither sees the unseal key or the other
# role's secret-id.
#
# SE_UID and SE_GID are the uid and gid the worker runs as, 65532 when unset.
# Both must be numbers other than 0. The owner of the worker's directory and
# secret-id is set to SE_UID on every run, so a volume made for another uid
# follows a changed SE_UID. Their group stays OpenBao's (1000), which reads
# them; SE_GID is only checked.
set -eu
umask 077

openbao_uid=100
openbao_gid=1000
web_uid=65532
worker_uid=${SE_UID:-65532}
worker_gid=${SE_GID:-65532}

# check_id NAME VALUE: fails unless VALUE is a number other than 0. The
# worker never runs as root.
check_id() {
	case $2 in
	'' | *[!0-9]*)
		echo "$1 must be a number, not '$2'" >&2
		exit 1
		;;
	esac
	if [ "$2" -eq 0 ]; then
		echo "$1 must not be 0: the worker does not run as root" >&2
		exit 1
	fi
}
check_id SE_UID "$worker_uid"
check_id SE_GID "$worker_gid"

# make_file DIR NAME OWNER MODE: runs the generator in the remaining arguments
# into DIR/NAME unless it exists. The file is written under a temporary name
# and renamed, so a stopped run never leaves a short file behind.
make_file() {
	dir=$1 name=$2 owner=$3 mode=$4
	shift 4
	mkdir -p "/secrets/$dir"
	if [ ! -e "/secrets/$dir/$name" ]; then
		"$@" >"/secrets/$dir/$name.tmp"
		chmod "$mode" "/secrets/$dir/$name.tmp"
		chown "$owner" "/secrets/$dir/$name.tmp"
		mv "/secrets/$dir/$name.tmp" "/secrets/$dir/$name"
		echo "created $dir/$name"
	fi
}

random_key() { head -c 32 /dev/urandom; }
random_hex() { od -An -tx1 -N32 /dev/urandom | tr -d ' \n'; }

make_file unseal key "$openbao_uid:$openbao_gid" 0400 random_key
make_file worker secret-id "$worker_uid:$openbao_gid" 0440 random_hex
make_file web secret-id "$web_uid:$openbao_gid" 0440 random_hex

chown "$openbao_uid:$openbao_gid" /secrets/unseal
chmod 0500 /secrets/unseal
chown "$worker_uid:$openbao_gid" /secrets/worker/secret-id
for dir in worker web; do
	uid=$web_uid
	if [ "$dir" = worker ]; then
		uid=$worker_uid
	fi
	chown "$uid:$openbao_gid" "/secrets/$dir"
	chmod 0550 "/secrets/$dir"
done
chmod 0755 /secrets
