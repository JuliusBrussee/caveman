package engine

import (
	"bytes"
	"unicode"
)

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
	leftTrimmed := bytes.TrimLeftFunc(input, unicode.IsSpace)
	start := len(input) - len(leftTrimmed)
	end := len(bytes.TrimRightFunc(input, unicode.IsSpace))
	trimmed := input[start:end]
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
	openEnd := fileOpeningTagEnd(trimmed)
	closeStart := len(trimmed) - len("</file>")
	// An unterminated open tag makes fileOpeningTagEnd walk into the closing
	// tag's own `>`; that is not a wrapper, and slicing on it would panic.
	if openEnd < 0 || openEnd+1 > closeStart {
		return input, fileWrapper{}
	}
	inner := trimmed[openEnd+1 : closeStart]
	if len(bytes.TrimSpace(inner)) == 0 {
		return input, fileWrapper{}
	}
	return inner, fileWrapper{
		prefix:  input[:start+openEnd+1],
		suffix:  input[start+closeStart:],
		present: true,
	}
}

// fileOpeningTagEnd finds the closing angle bracket without mistaking one in
// a quoted attribute value for the end of the tag. An unterminated quote makes
// the wrapper invalid and leaves the original untouched.
func fileOpeningTagEnd(input []byte) int {
	var quote byte
	for i := len("<file"); i < len(input); i++ {
		switch input[i] {
		case '\'', '"':
			if quote == 0 {
				quote = input[i]
			} else if quote == input[i] {
				quote = 0
			}
		case '>':
			if quote == 0 {
				return i
			}
		}
	}
	return -1
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

type inputWrapper interface {
	rewrap([]byte) []byte
}

type inputWrappers []inputWrapper

// unwrapInput removes supported presentation wrappers in outer-to-inner order.
// Four layers cover known agent compositions while bounding repeated scans of
// attacker-controlled input. rewrap applies them in reverse order.
func unwrapInput(input []byte) ([]byte, inputWrappers) {
	body := input
	wrappers := make(inputWrappers, 0, 2)
	for range 4 {
		if inner, wrapper := unwrapListing(body); wrapper.present {
			body = inner
			wrappers = append(wrappers, wrapper)
			continue
		}
		if inner, wrapper := unwrapFileWrapper(body); wrapper.present {
			body = inner
			wrappers = append(wrappers, wrapper)
			continue
		}
		break
	}
	return body, wrappers
}

func (w inputWrappers) rewrap(out []byte) []byte {
	for i := len(w) - 1; i >= 0; i-- {
		out = w[i].rewrap(out)
	}
	return out
}
