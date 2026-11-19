#!/bin/sh
# Downloads the scanner executables that scanners.lock pins for one platform,
# checks each download against its SHA-256 checksum, and installs them into a
# directory. Nothing is installed unless every download matches.
#
# Usage: fetch-scanners.sh <os> <arch> <dir>
#   os, arch  Go's names for the platform, for example linux arm64
#   dir       where the executables go; created if missing
#
# Needs curl, tar, and sha256sum or shasum.
set -eu

if [ "$#" -ne 3 ]; then
	echo "usage: fetch-scanners.sh <os> <arch> <dir>" >&2
	exit 2
fi
os=$1
arch=$2
dest=$3
lock="$(cd "$(dirname "$0")" && pwd)/scanners.lock"

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	else
		shasum -a 256 "$1" | cut -d ' ' -f 1
	fi
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/verified"

# The tools the lock file names, for any platform.
tools=$(grep -v -e '^#' -e '^[[:space:]]*$' "$lock" | cut -d ' ' -f 1 | sort -u)

while read -r tool version los larch sum url; do
	case $tool in '' | '#'*) continue ;; esac
	if [ "$los" != "$os" ] || [ "$larch" != "$arch" ]; then
		continue
	fi
	download="$work/$tool.download"
	curl --fail --silent --show-error --location \
		--proto '=https' --proto-redir '=https' --tlsv1.2 \
		--retry 3 --output "$download" "$url"
	got=$(sha256 "$download")
	if [ "$got" != "$sum" ]; then
		echo "fetch-scanners: checksum mismatch for $tool $version ($os/$arch): got $got, want $sum" >&2
		exit 1
	fi
	case $url in
	*.tar.gz)
		mkdir "$work/$tool"
		tar -xzf "$download" -C "$work/$tool" "$tool"
		mv "$work/$tool/$tool" "$work/verified/$tool"
		;;
	*)
		mv "$download" "$work/verified/$tool"
		;;
	esac
	echo "fetch-scanners: verified $tool $version for $os/$arch"
done <"$lock"

for tool in $tools; do
	if [ ! -f "$work/verified/$tool" ]; then
		echo "fetch-scanners: scanners.lock has no $tool for $os/$arch" >&2
		exit 1
	fi
done

mkdir -p "$dest"
for tool in $tools; do
	chmod 755 "$work/verified/$tool"
	# Copy next to the target and rename, so an interrupted copy never leaves
	# a partial executable under the tool's name.
	cp "$work/verified/$tool" "$dest/.$tool.partial"
	mv -f "$dest/.$tool.partial" "$dest/$tool"
	echo "fetch-scanners: installed $dest/$tool"
done
