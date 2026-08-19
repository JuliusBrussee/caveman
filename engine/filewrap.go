package engine

import "bytes"

// Coding agents wrap file references handed to the model: pi turns a @file
// argument into `<file name="...">…</file>`, and other agents use the same
// single-element wrapper convention. That wrapper is presentation, not content —
// the same way a read tool's line-number gutter is (see listing.go). Left in
// place it defeats Detect on the way in: the payload no longer starts with its
// real first bytes, so source never parses and JSON never validates; both fall
// through to `text` and compress nothing.
//
// Unwrapping belongs here rather than inside a compressor, for the same reason
// the listing gutter lives here: the wrapper is orthogonal to content type, and
// each compressor has to be routed on what the content actually is.
type fileWrapper struct {
	prefix  []byte
	suffix  []byte
	present bool
}

// unwrapFileWrapper separates a single whole-content `<file …>…</file>` wrapper
// from the content it decorates. It is deliberately conservative: it requires
// the wrapper to be the outermost element, to open with the literal `<file` tag
// (attributes allowed), to close with exactly one `</file>` at the end, and to
// contain non-whitespace content. Anything else returns the input unchanged and
// a no-op wrapper, so callers need no branches.
func unwrapFileWrapper(input []byte) ([]byte, fileWrapper) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) < 9 {
		return input, fileWrapper{}
	}
	if !bytes.HasPrefix(trimmed, []byte("<file")) {
		return input, fileWrapper{}
	}
	switch trimmed[5] {
	case ' ', '\t', '>':
	default:
		return input, fileWrapper{}
	}
	if !bytes.HasSuffix(trimmed, []byte("</file>")) {
		return input, fileWrapper{}
	}
	// Exactly one opening and one closing tag: a stray "<file" inside a string
	// literal or a nested element means this is not a plain wrapper, and the
	// original bytes win.
	if bytes.Count(trimmed, []byte("<file")) != 1 || bytes.Count(trimmed, []byte("</file>")) != 1 {
		return input, fileWrapper{}
	}
	openEnd := bytes.Index(trimmed, []byte(">"))
	if openEnd < 0 {
		return input, fileWrapper{}
	}
	inner := trimmed[openEnd+1 : len(trimmed)-len("</file>")]
	if len(bytes.TrimSpace(inner)) == 0 {
		return input, fileWrapper{}
	}
	return inner, fileWrapper{prefix: trimmed[:openEnd+1], suffix: []byte("</file>"), present: true}
}

// rewrap puts the wrapper back on compressed output so the model still sees the
// file reference the agent supplied, now wrapping the compressed bytes.
func (w fileWrapper) rewrap(out []byte) []byte {
	if !w.present {
		return out
	}
	var b bytes.Buffer
	b.Grow(len(w.prefix) + len(out) + len(w.suffix))
	b.Write(w.prefix)
	b.Write(out)
	b.Write(w.suffix)
	return b.Bytes()
}
