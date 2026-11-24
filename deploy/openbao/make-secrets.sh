#!/bin/sh
# Creates the AppRole secret-ids and OpenBao's TLS certificate in the
# openbao-secrets volume, mounted at /secrets. Each secret-id is created only
# when it is missing, and the certificate files are created together only
# when one of them is missing, so running this again never replaces a file
# that OpenBao, web or the worker already uses.
#
# Layout and owners:
#   /secrets/worker/secret-id   64 hex digits       $SE_UID:1000, 0440
#   /secrets/worker/ca.crt      CA certificate      $SE_UID:1000, 0444
#   /secrets/web/secret-id      64 hex digits       65532:1000, 0440
#   /secrets/web/ca.crt         CA certificate      65532:1000, 0444
#   /secrets/tls/tls.crt        server certificate  openbao (100:1000), 0444
#   /secrets/tls/tls.key        its private key     openbao (100:1000), 0400
#   /secrets/tls/ca.crt         CA certificate      openbao (100:1000), 0444
#
# OpenBao (uid 100, gid 1000) reads every file. The worker and web containers
# mount only their own directory, so neither sees OpenBao's private key or
# the other role's secret-id.
#
# The unseal key is not in this volume. It is a file on the host that
# deploy/openbao/unseal-key.sh makes and compose hands to OpenBao alone. A
# volume made before the key moved out still holds it at unseal/key; this
# script then stops, and unseal-key.sh moves the key out.
#
# The certificate is for the name openbao, which web and the worker use, and
# for 127.0.0.1, which the health check uses. The CA's private key is made in
# /tmp and is gone when this container ends, so no one can sign another
# certificate with it; a new certificate means removing the four certificate
# files, which makes a new CA. Both certificates last ten years: this is a
# local development stack, and nothing renews them.
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

if [ -e /secrets/unseal ]; then
	echo "the openbao-secrets volume still holds the unseal key; run 'make openbao-unseal-key' on the host to move it out" >&2
	exit 1
fi

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

random_hex() { od -An -tx1 -N32 /dev/urandom | tr -d ' \n'; }

make_file worker secret-id "$worker_uid:$openbao_gid" 0440 random_hex
make_file web secret-id "$web_uid:$openbao_gid" 0440 random_hex

# install_file SRC DIR NAME OWNER MODE: moves SRC to /secrets/DIR/NAME
# through a temporary name, so a stopped run never leaves a short file
# behind.
install_file() {
	src=$1 dir=$2 name=$3 owner=$4 mode=$5
	mkdir -p "/secrets/$dir"
	cp "$src" "/secrets/$dir/$name.tmp"
	chmod "$mode" "/secrets/$dir/$name.tmp"
	chown "$owner" "/secrets/$dir/$name.tmp"
	mv "/secrets/$dir/$name.tmp" "/secrets/$dir/$name"
	echo "created $dir/$name"
}

if [ ! -e /secrets/tls/tls.crt ] || [ ! -e /secrets/tls/tls.key ] || [ ! -e /secrets/tls/ca.crt ] ||
	[ ! -e /secrets/worker/ca.crt ] || [ ! -e /secrets/web/ca.crt ]; then
	work=/tmp/openbao-tls
	rm -rf "$work"
	mkdir -p "$work"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
		-keyout "$work/ca.key" -out "$work/ca.crt" -days 3650 \
		-subj "/CN=secret-externalizer local OpenBao CA" \
		-addext "basicConstraints=critical,CA:TRUE" \
		-addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
	openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
		-keyout "$work/tls.key" -out "$work/tls.csr" -subj "/CN=openbao" 2>/dev/null
	cat >"$work/tls.ext" <<'EXT'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=DNS:openbao,IP:127.0.0.1
EXT
	openssl x509 -req -in "$work/tls.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" \
		-CAcreateserial -days 3650 -extfile "$work/tls.ext" -out "$work/tls.crt" 2>/dev/null
	install_file "$work/tls.crt" tls tls.crt "$openbao_uid:$openbao_gid" 0444
	install_file "$work/tls.key" tls tls.key "$openbao_uid:$openbao_gid" 0400
	install_file "$work/ca.crt" tls ca.crt "$openbao_uid:$openbao_gid" 0444
	install_file "$work/ca.crt" worker ca.crt "$worker_uid:$openbao_gid" 0444
	install_file "$work/ca.crt" web ca.crt "$web_uid:$openbao_gid" 0444
	rm -rf "$work"
fi

chown "$openbao_uid:$openbao_gid" /secrets/tls
chmod 0500 /secrets/tls
chown "$worker_uid:$openbao_gid" /secrets/worker/secret-id /secrets/worker/ca.crt
for dir in worker web; do
	uid=$web_uid
	if [ "$dir" = worker ]; then
		uid=$worker_uid
	fi
	chown "$uid:$openbao_gid" "/secrets/$dir"
	chmod 0550 "/secrets/$dir"
done
chmod 0755 /secrets
