//go:build windows && amd64

package main

import _ "embed"

//go:embed assets/warp-helper-amd64.exe.gz
var bundledHelper []byte
