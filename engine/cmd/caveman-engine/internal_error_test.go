package main

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

// #1522 asked for the last of four fixes: `caveman-engine compress` answered an
// engine defect with a raw Go panic trace and the runtime's exit 2, which reads
// as a crashed tool. The reporter's own words: `caveman tools shrink` relayed it
// as "engine unavailable; passthrough — run caveman setup", sending the user to
// reinstall a working install. The first three fixes landed (the unwrap guard in
// engine/filewrap.go, and Server.safeEngineCall declining the segment and
// logging it in the proxy); this covers the CLI boundary.
func TestGuardInternalErrorReportsADefectReadably(t *testing.T) {
	var stderr bytes.Buffer
	code := guardInternalError("compress", &stderr, func() {
		panic("runtime error: slice bounds out of range [1:0]")
	})

	if code != exitInternalError {
		t.Fatalf("code = %d, want %d (non-zero and distinct from fatal()'s 1 and the unknown-subcommand 2)", code, exitInternalError)
	}
	if code == 0 || code == 1 || code == 2 {
		t.Fatalf("code = %d collides with an existing exit status", code)
	}

	out := stderr.String()
	// The subcommand and the defect, on the first line, before any stack.
	first := strings.SplitN(out, "\n", 2)[0]
	for _, want := range []string{"caveman-engine", `"compress"`, "slice bounds out of range [1:0]"} {
		if !strings.Contains(first, want) {
			t.Errorf("first stderr line %q missing %q", first, want)
		}
	}
	// It must not read as a configuration problem, and it must say where to go.
	for _, want := range []string{
		"nothing was written to stdout",
		"not a configuration problem",
		"github.com/JuliusBrussee/caveman/issues",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr missing %q:\n%s", want, out)
		}
	}
	// The stack is what makes the report actionable, so it still ships.
	if !strings.Contains(out, "guardInternalError") {
		t.Errorf("stderr carries no stack trace:\n%s", out)
	}
}

// A clean run must stay byte-identical on both streams: the guard is a backstop,
// not a wrapper that narrates.
func TestGuardInternalErrorIsSilentWithoutAPanic(t *testing.T) {
	var stderr bytes.Buffer
	ran := false
	if code := guardInternalError("detect", &stderr, func() { ran = true }); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !ran {
		t.Fatal("guardInternalError did not run the subcommand")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// Go 1.21+ turns panic(nil) into a *runtime.PanicNilError, so recover() is
// non-nil and the defect is still reported rather than silently exiting 0.
func TestGuardInternalErrorCatchesPanicNil(t *testing.T) {
	var stderr bytes.Buffer
	if code := guardInternalError("stats", &stderr, func() { panic(nil) }); code != exitInternalError {
		t.Fatalf("code = %d, want %d", code, exitInternalError)
	}
	if !strings.Contains(stderr.String(), "caveman-engine: internal error") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// The seam only helps if the entrypoint goes through it. A future edit that
// calls dispatch (or inlines the switch) straight from main would restore the
// raw trace while every test above still passed, so pin the wiring here — the
// same reason tests/verify_repo.py greps the PowerShell statusline.
func TestMainDispatchesThroughTheGuard(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := regexp.MustCompile(`(?s)\nfunc main\(\) \{.*?\n\}\n`).Find(src)
	if body == nil {
		t.Fatal("could not locate func main() in main.go")
	}
	if !bytes.Contains(body, []byte("guardInternalError(")) {
		t.Errorf("func main() does not route through guardInternalError:\n%s", body)
	}
	if !bytes.Contains(body, []byte("dispatch(")) {
		t.Errorf("func main() no longer calls dispatch:\n%s", body)
	}
	// The switch belongs to dispatch, so main cannot be running subcommands itself.
	if bytes.Contains(body, []byte("switch cmd")) {
		t.Errorf("func main() inlines the subcommand switch, bypassing the guard:\n%s", body)
	}
}
