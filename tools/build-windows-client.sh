#!/bin/sh

set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output_dir="$root_dir/dist"

mkdir -p "$output_dir"

(
	cd "$root_dir/windows"
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build -buildvcs=false -trimpath -ldflags '-s -w -buildid=' \
		-o "$output_dir/warp-windows.exe" .
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 \
		go build -buildvcs=false -trimpath -ldflags '-s -w -buildid=' \
		-o "$output_dir/warp-windows-arm64.exe" .
)

sha256sum "$output_dir/warp-windows.exe" "$output_dir/warp-windows-arm64.exe"
