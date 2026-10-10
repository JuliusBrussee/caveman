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

// GDScript arrives the same way and must reach the code compressor in both
// builds, gutter and all.
func TestGutteredGDScriptReachesTheCodeCompressor(t *testing.T) {
	e := New(nil, nil)
	source := "@tool\nextends Node2D\n\nsignal hit(damage: int)\n\n@export var speed: float = 120.0\n\n\nfunc _ready() -> void:\n\tvar t := get_tree()\n\tt.paused = false\n\tprint(speed)\n\n\nfunc take_hit(damage: int) -> void:\n\tif damage <= 0:\n\t\treturn\n\thit.emit(damage)\n\tqueue_free()\n"
	res := e.Simulate([]byte(gutterLines(source)), Options{Mode: ModeCompress})
	if res.ContentType != TypeCode {
		t.Fatalf("guttered GDScript detected as %q, want %q", res.ContentType, TypeCode)
	}
	if res.TokensSaved == 0 {
		t.Fatal("guttered GDScript compressed nothing")
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

// compressListing runs a guttered payload through the real pipeline and checks
// the gutter on what comes back: every line that came from the source carries
// the number that source line had, and a blank line carries a number only when
// it is that blank source line. Lines the compressor wrote itself (markers, the
// contract note) are skipped — they stand in for source lines, they are not one.
func compressListing(t *testing.T, source string) string {
	t.Helper()
	res, err := New(nil, nil).Compress([]byte(gutterLines(source)), Options{Mode: ModeCompress, ExternalRecovery: true})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Output)
	if !strings.Contains(out, "elided (caveman)") {
		t.Fatalf("fixture elided nothing; the test proves nothing:\n%s", out)
	}
	sourceLines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue // an introduced blank separator: no number is honest
		}
		gutter, rest, found := strings.Cut(line, "\t")
		number, err := strconv.Atoi(gutter)
		if !found || err != nil {
			t.Fatalf("output line lacks a gutter: %q", line)
		}
		if strings.Contains(rest, "(caveman)") || strings.HasPrefix(rest, "… caveman: ") {
			continue
		}
		if number < 1 || number > len(sourceLines) || sourceLines[number-1] != rest {
			t.Errorf("line %q numbered %d, which is not its source line", rest, number)
		}
	}
	return out
}

// A log whose text repeats every 420 lines: the line kept after a 645-line
// elision was numbered from its first identical copy (source line 648 printed
// as 228), because the elision marker never moved the matching cursor.
func TestListingKeepsTrueNumbersAfterElidingRepeatingLines(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 649; i++ {
		b.WriteString("2026-10-10T10:00:" + strconv.Itoa(100 + i%60)[1:] + "Z INFO worker-" + strconv.Itoa(i%4) +
			" processed batch id=batch-000" + strconv.Itoa(i%7) + " status=ok latency_ms=12 retries=0 queue=default region=us-east-1\n")
	}
	out := compressListing(t, b.String())
	if !strings.Contains(out, "\n648\t") {
		t.Fatalf("source line 648 is missing from the tail:\n%s", out)
	}
}

// The text compressor joins kept sections with blank lines a one-line-per-entry
// log never had. Those blanks took the cursor's number, so the gutter read
// 1, 2, 2, 3, 3, … — every number but the first printed twice.
func TestListingGivesIntroducedBlankLinesNoNumber(t *testing.T) {
	lines := []string{"> build", "Resolving dependencies..."}
	for i := 0; i < 300; i++ {
		lines = append(lines, "npm warn deprecated inflight@1.0.6: This module is not supported, and leaks memory.")
	}
	for i := 0; i < 300; i++ {
		lines = append(lines, "Retrying request to registry.npmjs.org (attempt 1)")
	}
	lines = append(lines,
		"npm warn deprecated inflight@1.0.6: This module is not supported, and leaks memory.",
		"Retrying request to registry.npmjs.org (attempt 1)",
		"ERR! code ETIMEDOUT",
		"ERR! network request to https://registry.npmjs.org/left-pad failed")
	out := compressListing(t, strings.Join(lines, "\n")+"\n")
	if !strings.Contains(out, "\n\n") {
		t.Fatalf("fixture introduced no blank separators; the test proves nothing:\n%s", out)
	}
}
