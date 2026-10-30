#!/bin/sh

set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output_dir="$root_dir/dist"
asset_dir="$root_dir/windows/assets"
helper_revision=$(cat "$root_dir/windows/helper-revision")
case "$helper_revision" in ''|*[!0-9]*) printf 'Invalid Windows helper revision\n' >&2; exit 1 ;; esac
helper_version=$(sed -n 's/^PKG_VERSION:=//p' "$root_dir/warp-warpscout/Makefile")-r$helper_revision

mkdir -p "$output_dir" "$asset_dir"

# Check both inputs before updating either embedded payload or client.
for architecture in amd64 arm64; do
	helper="$output_dir/warp-windows-helper-$helper_version-$architecture.exe"
	if [ ! -s "$helper" ]; then
		printf 'Missing Windows helper: %s\nBuild both helpers first.\n' "$helper" >&2
		exit 1
	fi
	case "$architecture:$(LC_ALL=C file -b "$helper")" in
		amd64:PE32+*x86-64*|arm64:PE32+*ARM64*) ;;
		*) printf 'Wrong executable architecture: %s\n' "$helper" >&2; exit 1 ;;
	esac
done

for architecture in amd64 arm64; do
	helper="$output_dir/warp-windows-helper-$helper_version-$architecture.exe"
	asset="$asset_dir/warp-helper-$architecture.exe.gz"
	gzip -n -9 -c "$helper" > "$asset.new"
	gzip -t "$asset.new"
	mv "$asset.new" "$asset"
done

(
	cd "$root_dir/windows"
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build -buildvcs=false -trimpath -ldflags '-s -w -buildid= -H=windowsgui' \
		-o "$output_dir/warp-windows.exe" .
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 \
		go build -buildvcs=false -trimpath -ldflags '-s -w -buildid= -H=windowsgui' \
		-o "$output_dir/warp-windows-arm64.exe" .
)

sha256sum "$output_dir/warp-windows.exe" "$output_dir/warp-windows-arm64.exe"
