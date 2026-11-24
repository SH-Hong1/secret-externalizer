#!/bin/sh
# Creates valkey's TLS certificate, its ACL file, and web's valkey password
# in the valkey-secrets volume, mounted at /secrets. The files are created
# together, only when one of them is missing, so running this again never
# replaces a password or certificate that valkey and web already use.
#
# Layout and owners:
#   /secrets/valkey/tls.crt    server certificate     valkey (999), 0444
#   /secrets/valkey/tls.key    its private key        valkey (999), 0400
#   /secrets/valkey/ca.crt     CA certificate         valkey (999), 0444
#   /secrets/valkey/users.acl  ACL users              valkey (999), 0400
#   /secrets/web/ca.crt        CA certificate         web (65532), 0444
#   /secrets/web/password      64 hex digits          web (65532), 0400
#
# The valkey and web containers mount only their own directory. The ACL
# file holds the SHA-256 of web's password, not the password. The CA's
# private key is made in /tmp and is gone when this container ends, so no
# one can sign another certificate with it; a new certificate means a new
# CA, made by removing the volume.
#
# The certificate is for the name valkey, which web uses, and for
# 127.0.0.1, which the health check uses. Both certificates last ten years:
# this is a local development stack, and nothing renews them.
set -eu
umask 077

valkey_uid=999
web_uid=65532
work=/tmp/valkey-secrets

if [ -e /secrets/valkey/tls.crt ] && [ -e /secrets/valkey/tls.key ] &&
	[ -e /secrets/valkey/ca.crt ] && [ -e /secrets/valkey/users.acl ] &&
	[ -e /secrets/web/ca.crt ] && [ -e /secrets/web/password ]; then
	echo "valkey secrets exist"
	exit 0
fi

rm -rf "$work"
mkdir -p "$work"

openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
	-keyout "$work/ca.key" -out "$work/ca.crt" -days 3650 \
	-subj "/CN=secret-externalizer local valkey CA" \
	-addext "basicConstraints=critical,CA:TRUE" \
	-addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
	-keyout "$work/tls.key" -out "$work/tls.csr" -subj "/CN=valkey" 2>/dev/null
cat >"$work/tls.ext" <<'EXT'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=DNS:valkey,IP:127.0.0.1
EXT
openssl x509 -req -in "$work/tls.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" \
	-CAcreateserial -days 3650 -extfile "$work/tls.ext" -out "$work/tls.crt" 2>/dev/null

password=$(openssl rand -hex 32)
printf '%s\n' "$password" >"$work/password"
hash=$(printf '%s' "$password" | openssl dgst -sha256 -r | cut -d ' ' -f 1)

# default: off, so a connection that has not signed in can run nothing.
# health: may only PING, for the container's health check; it has no
#   password because it can read and change nothing.
# se-web: web's user. It reaches only the session and login-attempt keys
#   and runs only the commands internal/session uses, and those that
#   valkey-go sends on connecting.
cat >"$work/users.acl" <<ACL
user default off resetpass resetkeys resetchannels -@all
user health on nopass resetkeys resetchannels -@all +ping
user se-web on #$hash resetkeys ~session:* ~attempt:* resetchannels -@all +hello +auth +ping +client|setinfo +client|setname +client|tracking +get +set +getdel +pexpire +del
ACL

# install SRC DIR NAME OWNER MODE: moves SRC to /secrets/DIR/NAME through a
# temporary name, so a stopped run never leaves a short file behind.
install_file() {
	src=$1 dir=$2 name=$3 owner=$4 mode=$5
	mkdir -p "/secrets/$dir"
	cp "$src" "/secrets/$dir/$name.tmp"
	chmod "$mode" "/secrets/$dir/$name.tmp"
	chown "$owner" "/secrets/$dir/$name.tmp"
	mv "/secrets/$dir/$name.tmp" "/secrets/$dir/$name"
	echo "created $dir/$name"
}

install_file "$work/tls.crt" valkey tls.crt "$valkey_uid:$valkey_uid" 0444
install_file "$work/tls.key" valkey tls.key "$valkey_uid:$valkey_uid" 0400
install_file "$work/ca.crt" valkey ca.crt "$valkey_uid:$valkey_uid" 0444
install_file "$work/users.acl" valkey users.acl "$valkey_uid:$valkey_uid" 0400
install_file "$work/ca.crt" web ca.crt "$web_uid:$web_uid" 0444
install_file "$work/password" web password "$web_uid:$web_uid" 0400
rm -rf "$work"

chown "$valkey_uid:$valkey_uid" /secrets/valkey
chmod 0500 /secrets/valkey
chown "$web_uid:$web_uid" /secrets/web
chmod 0500 /secrets/web
