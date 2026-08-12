//go:build !cgo

package evals_test

// cgoBuild reports whether this build includes the tree-sitter code compressor,
// which is required to compress the Python/TypeScript fixtures.
const cgoBuild = false
