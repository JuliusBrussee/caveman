// Package runtimeenv provides process-wide environment predicates shared by
// security gates. Keeping the production spelling in a dependency-free package
// avoids a cycle between platform/env and platform/kms while ensuring every
// service treats case and surrounding whitespace identically.
package runtimeenv

import (
	"os"
	"strings"
)

// IsProduction reports whether CAVE_ENV selects the managed production posture.
// Empty and unknown values remain local; only the canonical value (folded for
// operator-friendly case/whitespace) enables production refusals.
func IsProduction() bool {
	return IsProductionValue(os.Getenv("CAVE_ENV"))
}

// IsProductionValue applies the same predicate to an injected environment
// value, which keeps configuration loaders testable without a second spelling.
func IsProductionValue(raw string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), "prod")
}
