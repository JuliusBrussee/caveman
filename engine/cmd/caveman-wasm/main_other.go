//go:build !js || !wasm

// Host placeholder so `go build ./engine/...` succeeds on every platform.
// The real entrypoint is main.go, built only for js/wasm
// (`GOOS=js GOARCH=wasm go build ./engine/cmd/caveman-wasm`).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "caveman-wasm must be built with GOOS=js GOARCH=wasm (run: GOOS=js GOARCH=wasm go build ./engine/cmd/caveman-wasm)")
	os.Exit(1)
}
