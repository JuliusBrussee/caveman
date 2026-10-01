// Package providercatalog ships the dated provider catalog inside every binary
// that prices or sizes model traffic, so a lookup never depends on the working
// directory the binary happens to run from.
package providercatalog

import _ "embed"

// Current is catalog/current.yaml as of build time.
//
//go:embed catalog/current.yaml
var Current []byte
