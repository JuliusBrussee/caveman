package compressors

import (
	"bytes"
	"strconv"
)

// Coding agents almost never hand a compressor a bare source file. They hand it
// a FILE LISTING: the file's text with every line prefixed by its line number,
// the shape `cat -n` and every Read tool emit (Claude Code writes "%d\t", other
// harnesses pad the number and use a tab or " | " gutter). Those prefixes are
// not valid syntax in any language, so a parser-based compressor sees a file it
// cannot parse and passes it through untouched — silently giving up on the
// single most common payload in an agent session.
//
// SplitLineNumberedListing recognizes that shape and separates it into the
// source text and the per-line numbers, so the compressor can work on real
// source and the caller can put the gutter back afterwards.
//
// It is deliberately strict, because a false positive would corrupt content that
// merely begins with digits: every non-blank line must carry a number, the
// numbers must be strictly increasing, and there must be enough of them for the
// shape to be a listing rather than a coincidence.
const (
	// minListingLines is the shortest run that can identify a listing. Below it
	// an ordinary numbered prose list would qualify.
	minListingLines = 8
	// maxListingGutter bounds how wide a line-number gutter may be, which caps
	// how much padding a false positive could hide behind.
	maxListingGutter = 12
)

// SplitLineNumberedListing reports whether input is a line-numbered file
// listing. On success it returns the source text with gutters removed and the
// line number each source line carried.
func SplitLineNumberedListing(input []byte) (source []byte, numbers []int, ok bool) {
	lines := bytes.Split(input, []byte("\n"))
	// A trailing newline yields a final empty element that is not a real line.
	trailingNewline := len(lines) > 1 && len(lines[len(lines)-1]) == 0
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < minListingLines {
		return nil, nil, false
	}

	source = make([]byte, 0, len(input))
	numbers = make([]int, 0, len(lines))
	previous := 0
	numbered := 0
	for i, line := range lines {
		number, rest, found := splitLineNumberGutter(line)
		if !found {
			// A blank line inside a listing may legitimately carry no gutter.
			if len(bytes.TrimSpace(line)) == 0 {
				numbers = append(numbers, previous)
				if i > 0 {
					source = append(source, '\n')
				}
				continue
			}
			return nil, nil, false
		}
		if number <= previous {
			return nil, nil, false // not a monotonically increasing listing
		}
		previous = number
		numbered++
		numbers = append(numbers, number)
		if i > 0 {
			source = append(source, '\n')
		}
		source = append(source, rest...)
	}
	if numbered < minListingLines {
		return nil, nil, false
	}
	if trailingNewline {
		source = append(source, '\n')
	}
	return source, numbers, true
}

// splitLineNumberGutter parses one listing line into its number and the source
// text after the gutter. Recognized gutters are `<spaces><digits>\t`,
// `<spaces><digits> | ` and `<spaces><digits>: ` — the forms coding agents and
// `cat -n` actually emit. Anything else is not a gutter.
func splitLineNumberGutter(line []byte) (number int, rest []byte, ok bool) {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	start := i
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == start || i-start > maxListingGutter {
		return 0, nil, false
	}
	value, err := strconv.Atoi(string(line[start:i]))
	if err != nil {
		return 0, nil, false
	}
	switch {
	case i < len(line) && line[i] == '\t':
		return value, line[i+1:], true
	case i+2 < len(line) && line[i] == ' ' && line[i+1] == '|' && line[i+2] == ' ':
		return value, line[i+3:], true
	case i+1 < len(line) && line[i] == ':' && line[i+1] == ' ':
		return value, line[i+2:], true
	case i == len(line):
		// A bare number on its own line — a numbered blank line.
		return value, nil, true
	}
	return 0, nil, false
}

// RestoreLineNumberedListing puts the gutter back on a compressed listing.
//
// Compression removes lines, so output line N is not source line N. Renumbering
// the output 1..n would hand the model line numbers that point at the wrong
// code — worse than none, because they look authoritative. Instead each output
// line is matched forward against the source lines it came from and keeps the
// number it actually had. The result is a listing with GAPS where content was
// elided, which is both correct and a legible signal that something was removed.
//
// Lines the compressor introduced (body-elision markers, reflowed formatting)
// match nothing; they take the number of the source line the cursor is sitting
// on, which is where the elided region began.
//
// Some transforms do not preserve lines at all — re-encoding a JSON document
// rewrites nearly every one — and there the original numbering describes
// nothing. Rather than decorate restructured output with numbers that merely
// look authoritative, this reports ok=false when too few output lines can be
// traced back, and the caller emits the compressed body with no gutter. The
// content is already disclosed as transformed by its CCR marker; an honest
// absence of line numbers beats a confident fiction.
func RestoreLineNumberedListing(compressed, source []byte, numbers []int) ([]byte, bool) {
	sourceLines := bytes.Split(source, []byte("\n"))
	// A trailing newline yields a final empty element that is not a real line —
	// split it off the same way splitLineNumberedListing did, or the alignment
	// check below rejects every listing that ends in a newline (i.e. all of them).
	if len(sourceLines) > 1 && len(sourceLines[len(sourceLines)-1]) == 0 {
		sourceLines = sourceLines[:len(sourceLines)-1]
	}
	if len(numbers) < len(sourceLines) {
		// Cannot align: hand back the compressed body ungutted rather than
		// attach numbers that were not aligned.
		return compressed, false
	}
	index := indexSourceLines(sourceLines)

	outLines := bytes.Split(compressed, []byte("\n"))
	trailingNewline := len(outLines) > 1 && len(outLines[len(outLines)-1]) == 0
	if trailingNewline {
		outLines = outLines[:len(outLines)-1]
	}

	var out bytes.Buffer
	out.Grow(len(compressed) + len(outLines)*6)
	cursor := 0
	traceable, tracked := 0, 0
	for i, line := range outLines {
		position := cursor
		if position >= len(numbers) {
			position = len(numbers) - 1
		}
		number := numbers[position]
		// Blank lines are matched by nothing in particular — they occur
		// everywhere, and a compressor that reflows formatting (gofmt collapsing
		// the gap a removed body left) emits them in places the source never had
		// one. Letting them drive the cursor skips it past real code and
		// mis-numbers everything after. They take the cursor's number instead.
		if len(bytes.TrimSpace(line)) > 0 {
			tracked++
			if at, found := nextMatch(index, string(line), cursor); found {
				number = numbers[at]
				cursor = at + 1
				traceable++
			}
		}
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(strconv.Itoa(number))
		out.WriteByte('\t')
		out.Write(line)
	}
	if trailingNewline {
		out.WriteByte('\n')
	}
	if tracked > 0 && traceable*2 < tracked {
		// Fewer than half the substantive output lines came from a line this
		// source actually had. The transform restructured the content rather
		// than eliding parts of it, so the original numbering no longer
		// describes what the reader is looking at.
		return compressed, false
	}
	return out.Bytes(), true
}

// indexSourceLines maps each distinct source line to the ascending positions it
// occupies, so matching an output line is a lookup plus a short scan rather than
// a linear sweep of the whole file per line (quadratic on large listings).
func indexSourceLines(lines [][]byte) map[string][]int {
	index := make(map[string][]int, len(lines))
	for i, line := range lines {
		key := string(line)
		index[key] = append(index[key], i)
	}
	return index
}

// nextMatch finds the first occurrence of line at or after cursor.
func nextMatch(index map[string][]int, line string, cursor int) (int, bool) {
	positions, ok := index[line]
	if !ok {
		return 0, false
	}
	for _, position := range positions {
		if position >= cursor {
			return position, true
		}
	}
	return 0, false
}
