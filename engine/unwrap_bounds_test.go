package engine

import (
	"bytes"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// Codex cuts a long exec result at an arbitrary byte, and the cut can land
// inside the `"output":"` key itself. What is left — `{"exit_code":0,"output":"}`
// — still passes unwrapCodexExec's cheap shape checks: it opens with `{"`, it
// ends with `"}`, and it names exit_code. But the `"}` suffix check has already
// claimed the line's last two bytes, so the value's own opening quote IS that
// suffix quote and the value starts one byte past where it ends. Slicing those
// bounds panicked (`slice bounds out of range [25:24]`), which is #1522's
// filewrap.go panic reached through the sibling unwrapper that runs before it.
func TestUnwrapCodexExecLeavesALineCutInsideTheOutputKey(t *testing.T) {
	for _, in := range []string{
		`{"exit_code":0,"output":"}`,
		`{"exit_code":137,"output":"}`,
		`{"chunk_id":"0efd58","output":"}`,
		`{"chunk_id":"0efd58","wall_time_seconds":0.000014707,"exit_code":0,"original_token_count":1,"output":"}`,
		"Total output lines: 2\n\n" + `{"exit_code":0,"output":"}` + "\n",
	} {
		body, wrapper := unwrapCodexExec([]byte(in))
		if wrapper.present {
			t.Errorf("%q: unwrapped a line with no room for a value", in)
		}
		if !bytes.Equal(body, []byte(in)) {
			t.Errorf("%q: body = %q, want the input unchanged", in, body)
		}
	}
}

// The same line must reach the engine's two public entry points without taking
// the process down, and must fail toward the original: a payload the unwrapper
// declines is still a valid JSON object, so it is classified and compressed on
// its own merits rather than dropped.
func TestEngineSurvivesACodexLineCutInsideTheOutputKey(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(store, nil)

	in := []byte(`{"exit_code":0,"output":"}`)
	if got := eng.Detect(in); got == "" {
		t.Fatal("Detect returned no content type")
	}
	res, err := eng.Compress(in, Options{Mode: ModeCompress})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if !bytes.Equal(res.Output, in) {
		t.Errorf("Output = %q, want the original bytes passed through", res.Output)
	}
	if res.TokensAfter > res.TokensBefore {
		t.Errorf("TokensAfter %d > TokensBefore %d", res.TokensAfter, res.TokensBefore)
	}
}

// A whitespace-only output carries no content. Unwrapping to it handed the
// compressor a bare "\n" and left the JSON line itself unexamined, so nothing
// compressed; before the filewrap bounds guard landed it also walked straight
// into #1522's panic. The line stays whole instead.
func TestUnwrapCodexExecLeavesAWhitespaceOnlyOutput(t *testing.T) {
	for _, in := range []string{
		`{"exit_code":0,"output":"\n"}`,
		`{"exit_code":0,"output":"\t"}`,
		`{"exit_code":0,"output":"\r\n"}`,
		`{"exit_code":0,"output":" \t\n "}`,
		`{"exit_code":0,"output":""}`,
	} {
		body, wrapper := unwrapCodexExec([]byte(in))
		if wrapper.present {
			t.Errorf("%q: unwrapped to whitespace", in)
		}
		if !bytes.Equal(body, []byte(in)) {
			t.Errorf("%q: body = %q, want the input unchanged", in, body)
		}
	}
}

// A real output is still unwrapped and still rewraps byte-exactly, so the
// guards above cost the working path nothing.
func TestUnwrapCodexExecStillUnwrapsRealOutput(t *testing.T) {
	in := []byte(`{"exit_code":0,"output":"INFO one\nINFO two\n"}`)
	body, wrapper := unwrapCodexExec(in)
	if !wrapper.present {
		t.Fatal("did not unwrap a real output")
	}
	if got, want := string(body), "INFO one\nINFO two\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if got := wrapper.rewrap(body); !bytes.Equal(got, in) {
		t.Fatalf("rewrap = %q, want %q", got, in)
	}
}

// FuzzUnwrapInput guards the whole presentation-wrapper chain, not just the
// line above: every unwrapper slices attacker-shaped bytes (a tool result is
// whatever the command printed), and a panic here is a dropped request the
// proxy answers with an empty reply. The seeds are the shapes that have
// actually panicked in the field (#1508, #1522) plus one of each wrapper.
func FuzzUnwrapInput(f *testing.F) {
	for _, seed := range []string{
		"",
		" ",
		"\n",
		`{"exit_code":0,"output":"}`,
		`{"exit_code":0,"output":"\n"}`,
		`{"exit_code":0,"output":""}`,
		`{"chunk_id":"a","output":"x"}`,
		"Total output lines: 2\n\n" + `{"exit_code":0,"output":"a\nb\n"}` + "\n",
		`<file name="a.go">package main</file>`,
		"<file>\n</file>",
		"<file",
		"</file>",
		"1\t{\n2\t  \"a\": 1\n",
		`<file name="a"><file name="b">x</file></file>`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		body, wrappers := unwrapInput(in)
		if len(body) > len(in) {
			t.Fatalf("unwrap grew the input: %d > %d", len(body), len(in))
		}
		_ = wrappers.rewrap(body)
	})
}
