package compressors

import "bytes"

// Line-ending helpers shared by the line-oriented compressors.
//
// The old pattern here was `sep := "\n"; if bytes.Contains(input, "\r\n") { sep
// = "\r\n" }` — a single CRLF anywhere decided the separator for the whole
// document. On mixed input (a Windows tool's output pasted next to Unix output,
// which is what a CI transcript or `curl -i` capture looks like) that either
// rewrote every LF to CRLF or split the document into one giant "line" and
// silently compressed nothing.
//
// splitLines/joinLines split on LF only and leave each line's own CR attached,
// so rejoining reproduces every ending byte-for-byte. Synthesized lines (elision
// markers) get their ending from crSuffix, which follows the document majority.

// splitLines splits input on LF, leaving any CR attached to the end of its own
// line. trailing reports whether the input ended with a line break.
func splitLines(input []byte) (lines [][]byte, trailing bool) {
	body := input
	trailing = bytes.HasSuffix(body, []byte("\n"))
	if trailing {
		body = body[:len(body)-1]
	}
	return bytes.Split(body, []byte("\n")), trailing
}

// joinLines is the inverse of splitLines.
func joinLines(lines [][]byte, trailing bool) []byte {
	out := bytes.Join(lines, []byte("\n"))
	if trailing {
		out = append(out, '\n')
	}
	return out
}

// majorityCRLF reports whether most of the line breaks in input are CRLF.
func majorityCRLF(input []byte) bool {
	lf := bytes.Count(input, []byte("\n"))
	return lf > 0 && bytes.Count(input, []byte("\r\n"))*2 > lf
}

// crSuffix returns the CR a synthesized line needs so it matches the endings the
// rest of the document uses.
func crSuffix(input []byte) []byte {
	if majorityCRLF(input) {
		return []byte("\r")
	}
	return nil
}

// synthLine builds a synthesized line (an elision marker) with the document's
// dominant line ending.
func synthLine(text string, cr []byte) []byte {
	return append([]byte(text), cr...)
}
