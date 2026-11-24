#!/bin/sh
# Makes the file that holds the local OpenBao's unseal key, outside every
# Docker volume. Run it on the host, once, before the first
# `docker compose up`; `make openbao-unseal-key` runs it.
#
# The file is OPENBAO_UNSEAL_KEY_FILE, or
# $HOME/.config/secret-externalizer/openbao-unseal.key when that is unset,
# the same default compose.yaml uses. It holds 32 random bytes.
#
# - When the file exists, it is checked and not changed.
# - When it does not and the openbao-secrets volume holds a key at
#   unseal/key, that key is copied to the file.
# - When neither holds a key but the openbao-data volume exists, it stops:
#   that storage was sealed with a key that is gone, and a new key would not
#   open it.
# - Otherwise a new key is made.
#
# Then, when the volume holds an unseal/ folder, its key is removed only if
# it has the same bytes as the file, and the folder with it. A key that
# differs is left in place and the script fails.
#
# The folder of the file is made with mode 0700 and must not let anyone but
# its owner enter it. The file itself is 0644 so that OpenBao, which runs as
# uid 100 in its container, can read it through the bind mount on a Linux
# host, where the mount keeps the host's owner and mode; the folder keeps
# other users of the host out.
set -eu

key=${OPENBAO_UNSEAL_KEY_FILE:-${HOME:?HOME is not set}/.config/secret-externalizer/openbao-unseal.key}
project=secret-externalizer
secrets_volume=${project}_openbao-secrets
data_volume=${project}_openbao-data
# The openbao image of compose.yaml; change both together.
image=openbao/openbao:2.7.2@sha256:5046911075e137e154ce49ab051502f4ec7cf367d484ae4a4dce665d9b90561f
docker=${DOCKER:-docker}

case $key in
/*) ;;
*)
	echo "OPENBAO_UNSEAL_KEY_FILE must be an absolute path, not '$key'" >&2
	exit 1
	;;
esac
dir=$(dirname "$key")

# check_size FILE: fails unless FILE holds exactly 32 bytes.
check_size() {
	size=$(wc -c <"$1" | tr -d ' ')
	if [ "$size" -ne 32 ]; then
		echo "$1 holds $size bytes, not the 32 bytes of an unseal key" >&2
		exit 1
	fi
}

# check_dir: fails when someone other than the owner may enter the folder.
check_dir() {
	if [ -n "$(find "$dir" -maxdepth 0 -perm -0010)" ] || [ -n "$(find "$dir" -maxdepth 0 -perm -0001)" ]; then
		echo "$dir lets other users enter it; make it 0700 (chmod 0700 '$dir') or choose another OPENBAO_UNSEAL_KEY_FILE" >&2
		exit 1
	fi
}

# volume_key: prints the volume's unseal key, or exits 3 when the volume
# has none. OpenBao's user owns the key and its folder.
volume_key() {
	"$docker" run --rm --network none --user 100:1000 --read-only \
		--volume "$secrets_volume:/secrets:ro" --entrypoint sh "$image" \
		-c 'if [ -e /secrets/unseal/key ]; then cat /secrets/unseal/key; else exit 3; fi'
}

# make_key: writes the key file from the volume's copy when there is one,
# or from new random bytes, and sets source to "volume" or "new".
make_key() {
	if [ ! -d "$dir" ]; then
		mkdir -p -m 0700 "$dir"
	fi
	check_dir
	tmp="$key.tmp.$$"
	trap 'rm -f "$tmp"' EXIT
	umask 022
	source=new
	if "$docker" volume inspect "$secrets_volume" >/dev/null 2>&1; then
		status=0
		volume_key >"$tmp" || status=$?
		case $status in
		0) source=volume ;;
		3) ;;
		*)
			echo "reading the unseal key from the volume $secrets_volume failed" >&2
			exit 1
			;;
		esac
	fi
	if [ "$source" = new ]; then
		if "$docker" volume inspect "$data_volume" >/dev/null 2>&1; then
			echo "the volume $data_volume exists but no unseal key was found in $key or in $secrets_volume;" >&2
			echo "a new key cannot open that storage. Put the key it was made with at $key," >&2
			echo "or remove both volumes to start empty (this deletes every value OpenBao holds)." >&2
			exit 1
		fi
		head -c 32 /dev/urandom >"$tmp"
	fi
	check_size "$tmp"
	chmod 0644 "$tmp"
	mv "$tmp" "$key"
	trap - EXIT
	if [ "$source" = new ]; then
		echo "created a new unseal key at $key"
	else
		echo "copied the unseal key from the volume $secrets_volume to $key"
	fi
}

if [ -e "$key" ]; then
	check_dir
	check_size "$key"
	echo "the unseal key exists at $key"
else
	make_key
fi

# The volume's copy is removed only when the file holds the same bytes, so a
# volume whose key differs from the file keeps it and this script fails.
# Root is needed because the key's folder belongs to OpenBao's user and the
# volume's top folder to root.
if ! "$docker" volume inspect "$secrets_volume" >/dev/null 2>&1; then
	exit 0
fi
status=0
"$docker" run --rm --network none --user 0:0 --read-only --cap-drop ALL --cap-add DAC_OVERRIDE \
	--volume "$secrets_volume:/secrets" --volume "$key:/host-key:ro" --entrypoint sh "$image" \
	-c 'if [ ! -e /secrets/unseal ]; then exit 3; fi; if [ -e /secrets/unseal/key ]; then cmp -s /secrets/unseal/key /host-key || exit 4; rm /secrets/unseal/key; fi; rmdir /secrets/unseal' ||
	status=$?
case $status in
0) echo "removed the unseal key from the volume $secrets_volume" ;;
3) ;;
4)
	echo "the volume $secrets_volume holds an unseal key that differs from $key; nothing was changed." >&2
	echo "Keep the key that opens the openbao-data volume at $key and run this script again." >&2
	exit 1
	;;
*)
	echo "removing the unseal key from the volume $secrets_volume failed; run this script again to retry" >&2
	exit 1
	;;
esac
