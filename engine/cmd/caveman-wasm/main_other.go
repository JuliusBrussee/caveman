//go:build !js || !wasm

// Host placeholder so `go build ./public/engine/...` succeeds on every platform.
// The real entrypoint is main.go, built only for js/wasm (`make engine-wasm`).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "caveman-wasm must be built with GOOS=js GOARCH=wasm (run: make engine-wasm)")
	os.Exit(1)
}
