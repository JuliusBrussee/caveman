package rewriter

import (
	"strings"
	"testing"
)

func TestAcceptPreservesCompleteWindowsSourceCoordinates(t *testing.T) {
	for _, tt := range []struct {
		name      string
		location  string
		corrupted string
	}{
		{"drive", `C:\repo\module.go:42`, `D:\repo\module.go:42`},
		{"drive with spaces", `C:\Project Files\repo\module.go:42`, `D:\Different Files\repo\module.go:42`},
		{"forward slash drive", `C:/Project Files/repo/module.go:42:8`, `D:/Project Files/repo/module.go:42:8`},
		{"UNC server", `\\build-server\source\repo\module.go:42`, `\\other-server\source\repo\module.go:42`},
		{"UNC share with spaces", `\\build-server\Project Files\repo\module.go:42`, `\\build-server\Other Files\repo\module.go:42`},
		{"forward slash UNC", `//build-server/Project Files/repo/module.go:42`, `//other-server/Project Files/repo/module.go:42`},
		{"MSVC drive", `C:\Project Files\src\main.cpp(42,8)`, `D:\Project Files\src\main.cpp(42,8)`},
		{"MSVC UNC", `\\build-server\Project Files\src\main.cpp(42,8)`, `\\other-server\Project Files\src\main.cpp(42,8)`},
		{"parenthesized directory", `C:\Program Files (x86)\src\main.cpp(42,8)`, `D:\Program Files (x86)\src\main.cpp(42,8)`},
		{"extensionless Windows frame", `C:\Project Files\Makefile:42`, `D:\Project Files\Makefile:42`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := []byte("panic: task crashed\n\t" + tt.location + "\n" + strings.Repeat("ok compiled routine information. ", 300))
			preserved := []byte("panic: task crashed\n" + tt.location)
			if ok, reason := Accept(original, preserved, 1); !ok {
				t.Fatalf("unchanged source coordinate must allow filler removal: %s", reason)
			}
			corrupted := []byte("panic: task crashed\n" + tt.corrupted)
			if ok, reason := Accept(original, corrupted, 1); ok || reason != reasonReference {
				t.Fatalf("changed full source coordinate was accepted: ok=%v reason=%q", ok, reason)
			}
		})
	}
}

func TestWindowsCoordinateExtractionKeepsUnixAndFrameBoundaries(t *testing.T) {
	const first = `C:\Project Files\src\main.cpp(42,8)`
	const second = `D:\other\module.go:19:3`
	text := "frame: " + first + ": diagnostic\n\t" + second + " +0x19\n/tmp/repo/module.go:7"
	locations := sourceLocations(text)
	for _, want := range []string{first, second, "/tmp/repo/module.go:7"} {
		found := false
		for _, got := range locations {
			if got == want {
				found = true
			}
			if strings.Contains(got, "\n") || strings.Contains(got, "diagnostic") || strings.Contains(got, "frame:") {
				t.Fatalf("coordinate extraction swallowed surrounding output: %q", got)
			}
		}
		if !found {
			t.Fatalf("missing full coordinate %q in %q", want, locations)
		}
	}
}

func TestAcceptRejectsNumericPrefixesOfSourceCoordinates(t *testing.T) {
	for _, tt := range []struct {
		original string
		changed  string
	}{
		{`C:\repo\module.go:42`, `C:\repo\module.go:420`},
		{`C:\repo\module.go:42:8`, `C:\repo\module.go:42:80`},
		{`\\server\share\module.go:42`, `\\server\share\module.go:420`},
		{`src/module.go:42`, `src/module.go:420`},
		{`/app/src/module.go:42:8`, `/app/src/module.go:42:80`},
		{`Makefile:42`, `Makefile:420`},
		{`C:\src\module.cpp(42,8)`, `C:\src\module.cpp(420,8)`},
		{`File "/app/module.py", line 42`, `File "/app/module.py", line 420`},
	} {
		t.Run(tt.original, func(t *testing.T) {
			original := []byte("panic: task crashed\n" + tt.original + "\n" + strings.Repeat("ok compiled routine information. ", 300))
			preserved := []byte("panic: task crashed\n" + tt.original)
			if ok, reason := Accept(original, preserved, 1); !ok {
				t.Fatalf("preserved complete coordinate was rejected: %s", reason)
			}
			changed := []byte("panic: task crashed\n" + tt.changed)
			if ok, reason := Accept(original, changed, 1); ok || reason != reasonReference {
				t.Fatalf("coordinate prefix was accepted as the complete token: ok=%v reason=%q", ok, reason)
			}
		})
	}
}
