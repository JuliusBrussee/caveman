package compressors

import (
	"strings"
	"testing"
)

func TestLogFastFilterMatchesCanonicalRegexp(t *testing.T) {
	words := []string{"ERROR", "FATAL", "PANIC", "EXCEPTION", "TRACEBACK", "FAIL", "FAILED", "FAILURE", "WARN", "WARNING",
		"ſailed", "tracebacK", "caused by", "file.go:12", "file.GO:12", "  at frame", "\tFile \"x.py\"", "  --->", "ordinary details", "", "é", "\xff"}
	for _, word := range words {
		for _, spelling := range []string{word, strings.ToLower(word), strings.ToUpper(word)} {
			for _, prefix := range []string{"", "[INFO] ", "x", "_", " ", "\t", "\n", "\f", "é"} {
				for _, suffix := range []string{"", " details", "x", "_", ":", "é"} {
					line := []byte(prefix + spelling + suffix)
					if got, want := importantLogLine(line), importantLineRe.Match(line); got != want {
						t.Fatalf("filter changed kept line %q: got %v want %v", line, got, want)
					}
				}
			}
		}
	}
}
