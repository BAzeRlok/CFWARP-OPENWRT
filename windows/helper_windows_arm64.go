//go:build windows && arm64

package main

import _ "embed"

//go:embed assets/warp-helper-arm64.exe.gz
var bundledHelper []byte
