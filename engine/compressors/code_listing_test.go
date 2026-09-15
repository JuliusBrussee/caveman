package compressors

import (
	"strconv"
	"strings"
	"testing"
)

const listingSource = `package sample

import "fmt"

func alpha(a int) int {
	total := 0
	for i := 0; i < a; i++ {
		total += i
	}
	return total
}

func beta(b string) string {
	trimmed := b
	return trimmed + "!"
}
`

// gutter renders source the way a coding agent's read tool does.
func gutter(source, sep string) string {
	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	var out []string
	for i, line := range lines {
		out = append(out, strconv.Itoa(i+1)+sep+line)
	}
	return strings.Join(out, "\n") + "\n"
}

func TestSplitLineNumberedListingRoundTrips(t *testing.T) {
	for _, sep := range []string{"\t", " | ", ": "} {
		source, numbers, ok := SplitLineNumberedListing([]byte(gutter(listingSource, sep)))
		if !ok {
			t.Fatalf("sep %q: listing not recognized", sep)
		}
		if string(source) != listingSource {
			t.Fatalf("sep %q: source mismatch:\n%q", sep, source)
		}
		if numbers[0] != 1 {
			t.Fatalf("sep %q: first number = %d, want 1", sep, numbers[0])
		}
	}
}

// Recognition must be strict: a false positive corrupts content that merely
// begins with digits.
func TestSplitLineNumberedListingRejectsNonListings(t *testing.T) {
	cases := map[string]string{
		"plain source":     listingSource,
		"too short":        "1\ta\n2\tb\n3\tc\n",
		"non-monotonic":    "1\ta\n2\tb\n2\tc\n4\td\n5\te\n6\tf\n7\tg\n8\th\n9\ti\n",
		"unnumbered line":  "1\ta\n2\tb\n3\tc\nplain\n5\te\n6\tf\n7\tg\n8\th\n9\ti\n",
		"no gutter after":  "1a\n2b\n3c\n4d\n5e\n6f\n7g\n8h\n9i\n",
		"prose numbering":  "1. first\n2. second\n3. third\n4. fourth\n5. fifth\n6. sixth\n7. seventh\n8. eighth\n",
		"decreasing after": "10\ta\n9\tb\n8\tc\n7\td\n6\te\n5\tf\n4\tg\n3\th\n",
	}
	for name, input := range cases {
		if _, _, ok := SplitLineNumberedListing([]byte(input)); ok {
			t.Errorf("%s: unexpectedly recognized as a listing", name)
		}
	}
}

// A line-eliding transform keeps the numbers the source actually had, gaps and
// all. Renumbering 1..n would point the model at the wrong code while looking
// authoritative.
func TestRestoreKeepsOriginalNumbersOnLineElidingOutput(t *testing.T) {
	sourceLines := strings.Split(strings.TrimSuffix(listingSource, "\n"), "\n")
	numbers := make([]int, len(sourceLines))
	for i := range numbers {
		numbers[i] = i + 1
	}
	// Elide the two function bodies, keeping every other line verbatim.
	var kept []string
	for _, line := range sourceLines {
		if strings.HasPrefix(line, "\t") {
			continue
		}
		kept = append(kept, line)
	}
	out, ok := RestoreLineNumberedListing([]byte(strings.Join(kept, "\n")), []byte(listingSource), numbers)
	if !ok {
		t.Fatal("line-eliding output was refused a gutter")
	}
	for _, line := range strings.Split(string(out), "\n") {
		number, rest, found := splitLineNumberGutter([]byte(line))
		if !found {
			t.Fatalf("output line lacks a gutter: %q", line)
		}
		if len(strings.TrimSpace(string(rest))) == 0 {
			continue
		}
		if sourceLines[number-1] != string(rest) {
			t.Errorf("line %q numbered %d, but source line %d is %q",
				rest, number, number, sourceLines[number-1])
		}
	}
}

// A restructuring transform (re-encoded JSON) leaves no line the original
// numbering describes. Decorating it with numbers would be a confident fiction,
// so the gutter is declined instead.
func TestRestoreDeclinesGutterOnRestructuredOutput(t *testing.T) {
	source := []byte("{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3,\n  \"d\": 4,\n  \"e\": 5,\n  \"f\": 6,\n  \"g\": 7\n}\n")
	numbers := []int{1, 2, 3, 4, 5, 6, 7, 8, 9}
	restructured := []byte(`{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"g":7}`)
	out, ok := RestoreLineNumberedListing(restructured, source, numbers)
	if ok {
		t.Fatalf("restructured output was gutted with numbers that describe nothing: %q", out)
	}
	if string(out) != string(restructured) {
		t.Fatalf("declined restore altered the body: %q", out)
	}
}
