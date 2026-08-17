package engine

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// gutterLines renders content the way a coding agent's read tool prints a file.
func gutterLines(content string) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strconv.Itoa(i+1) + "\t" + line
	}
	return strings.Join(out, "\n") + "\n"
}

func jsonFixture(t *testing.T) string {
	t.Helper()
	type row struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Region string `json:"region"`
		Active bool   `json:"active"`
	}
	rows := make([]row, 60)
	for i := range rows {
		rows[i] = row{ID: i, Name: "node-" + strconv.Itoa(i), Region: "eu-west-1", Active: i%2 == 0}
	}
	raw, err := json.MarshalIndent(map[string]any{"nodes": rows}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw) + "\n"
}

// Detect runs on the content, not on the read tool's gutter. Before this, a
// guttered JSON document did not start with '{', json.Valid failed, and it
// routed to text — compressing nothing.
func TestDetectSeesThroughTheReadToolGutter(t *testing.T) {
	e := New(nil, nil)
	content := jsonFixture(t)
	if got := e.Detect([]byte(content)); got != TypeJSON {
		t.Fatalf("plain content detected as %q, want %q", got, TypeJSON)
	}
	if got := e.Detect([]byte(gutterLines(content))); got != TypeJSON {
		t.Fatalf("guttered content detected as %q, want %q — the gutter is hiding the content type", got, TypeJSON)
	}
}

// The whole point: a file the agent read compresses about as well as the file
// itself. Accounting stays against the bytes the caller actually supplied.
func TestGutteredContentCompressesLikeItsContent(t *testing.T) {
	e := New(nil, nil)
	content := jsonFixture(t)

	plain := e.Simulate([]byte(content), Options{Mode: ModeCompress})
	guttered := e.Simulate([]byte(gutterLines(content)), Options{Mode: ModeCompress})

	if plain.TokensSaved == 0 {
		t.Fatal("fixture does not compress at all; the test proves nothing")
	}
	if guttered.TokensSaved == 0 {
		t.Fatal("guttered content compressed nothing — the read tool's gutter is still hiding it")
	}
	// Within a wide band: the gutter itself costs tokens, so the ratios differ.
	if guttered.Ratio < plain.Ratio/2 {
		t.Fatalf("guttered ratio %.3f is far below plain %.3f", guttered.Ratio, plain.Ratio)
	}
	if guttered.TokensBefore != e.counter.Count([]byte(gutterLines(content))) {
		t.Fatal("TokensBefore must count the bytes the caller supplied, gutter included")
	}
}

// Guttered source must reach the code compressor too — that path is what a
// coding agent's Read of a source file produces.
func TestGutteredSourceReachesTheCodeCompressor(t *testing.T) {
	e := New(nil, nil)
	source := `package sample

import "fmt"

func alpha(a int) int {
	total := 0
	for i := 0; i < a; i++ {
		total += i * 3
	}
	fmt.Println(total)
	return total
}

func beta(b string) string {
	trimmed := b + "-suffix"
	return trimmed + "!"
}
`
	res := e.Simulate([]byte(gutterLines(source)), Options{Mode: ModeCompress})
	if res.ContentType != TypeCode {
		t.Fatalf("guttered source detected as %q, want %q", res.ContentType, TypeCode)
	}
	if res.TokensSaved == 0 {
		t.Fatal("guttered source compressed nothing")
	}
}

// Content that is not a listing must be completely unaffected.
func TestNonListingContentIsUntouched(t *testing.T) {
	content := jsonFixture(t)
	body, wrapper := unwrapListing([]byte(content))
	if wrapper.present {
		t.Fatal("plain content was mistaken for a listing")
	}
	if string(body) != content {
		t.Fatal("plain content was altered")
	}
	if got := string(wrapper.rewrap([]byte("unchanged"))); got != "unchanged" {
		t.Fatalf("rewrap on a non-listing altered output: %q", got)
	}
}

// record mode never transforms, gutter or no gutter.
func TestRecordModeStillNeverTransformsAListing(t *testing.T) {
	e := New(nil, nil)
	input := []byte(gutterLines(jsonFixture(t)))
	res, err := e.Compress(input, Options{Mode: ModeRecord})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Output) != string(input) {
		t.Fatal("record mode transformed a listing")
	}
	if res.TokensAfter != res.TokensBefore {
		t.Fatal("record mode claimed a reduction")
	}
}
