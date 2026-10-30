#!/bin/sh

set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
recipe="$root_dir/warp-warpscout/Makefile"
version=$(sed -n 's/^PKG_VERSION:=//p' "$recipe")
revision=$(sed -n 's/^PKG_RELEASE:=//p' "$recipe")
expected=$(sed -n 's/^PKG_HASH:=//p' "$recipe")
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/warp-windows-helper.XXXXXX")
trap 'rm -rf -- "$work_dir"' EXIT HUP INT TERM

archive=${WARP_SCOUT_ARCHIVE:-}
if [ -z "$archive" ]; then
	archive="$work_dir/warpscout.tar.gz"
	curl -fL --connect-timeout 15 --max-time 300 \
		"https://codeload.github.com/vernette/warpscout/tar.gz/refs/tags/v$version" \
		-o "$archive"
fi
actual=$(sha256sum "$archive" | awk '{ print $1 }')
[ "$actual" = "$expected" ] || {
	printf 'Source archive SHA-256 mismatch: %s\n' "$archive" >&2
	exit 1
}

tar -xzf "$archive" -C "$work_dir"
source_dir="$work_dir/warpscout-$version"
for source_patch in "$root_dir"/warp-warpscout/patches/*.patch; do
	patch --batch --fuzz=0 -d "$source_dir" -p1 < "$source_patch"
done

mkdir -p "$root_dir/dist"
for architecture in amd64 arm64; do
	(
		cd "$source_dir"
		CGO_ENABLED=0 GOOS=windows GOARCH="$architecture" \
			go build -buildvcs=false -trimpath \
			-ldflags "-s -w -buildid= -X main.version=$version-r$revision" \
			-o "$root_dir/dist/warp-windows-helper-$version-r$revision-$architecture.exe" .
	)
done

sha256sum "$root_dir/dist/warp-windows-helper-$version-r$revision-amd64.exe" \
	"$root_dir/dist/warp-windows-helper-$version-r$revision-arm64.exe"
