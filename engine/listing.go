package engine

import "github.com/JuliusBrussee/caveman/engine/compressors"

// Coding agents do not hand this engine files. They hand it what their read tool
// printed: the file with every line prefixed by its number — `1\t{`, `2\t  "unit"`.
// That gutter is part of the presentation, not part of the content, and it defeats
// Detect on the way in. A JSON document arrives not starting with `{`, so
// json.Valid fails and it never reaches the JSON compressor; source arrives
// unparseable, so it never reaches the code compressor. Both fall through to
// `text` and compress nothing.
//
// Unwrapping belongs here rather than inside a compressor, because the gutter is
// orthogonal to content type: it wraps JSON, source, logs, CSV and config alike,
// and each one has to be routed on what it actually is.
type listingWrapper struct {
	source  []byte
	numbers []int
	present bool
}

// unwrapListing separates a line-number gutter from the content it decorates.
// When the input is not a listing it returns the input unchanged and a wrapper
// that rewraps to a no-op, so callers need no branches.
func unwrapListing(input []byte) ([]byte, listingWrapper) {
	source, numbers, ok := compressors.SplitLineNumberedListing(input)
	if !ok {
		return input, listingWrapper{}
	}
	return source, listingWrapper{source: source, numbers: numbers, present: true}
}

// rewrap puts the gutter back on compressed output, keeping each surviving
// line's ORIGINAL number so the numbers still point at the file the agent read.
// It declines when the transform restructured the content rather than eliding
// parts of it — re-encoded JSON has no line that the original numbering
// describes — and then returns the compressed body bare. That is honest: the
// payload already carries a CCR marker saying it was transformed, and no line
// numbers beat numbers that merely look authoritative.
func (l listingWrapper) rewrap(out []byte) []byte {
	if !l.present {
		return out
	}
	rewrapped, ok := compressors.RestoreLineNumberedListing(out, l.source, l.numbers)
	if !ok {
		return out
	}
	return rewrapped
}
