package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMain drops the agents' relocation variables so a test run from inside a
// relocated Claude Code or Codex never falls through to the real config dir.
func TestMain(m *testing.M) {
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	os.Unsetenv("CODEX_HOME")
	os.Exit(m.Run())
}

func TestAgentRootsHonorRelocationVariables(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", "")
	t.Setenv("CAVEMAN_CODEX_ROOT", "")
	t.Setenv("CAVEMAN_CLAUDE_GLOBAL_CONFIG", "")
	claude, codex := filepath.Join(base, "claude-max5"), filepath.Join(base, "codex-alt")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	if claudeRoot() != claude || codexRoot() != codex || claudeGlobalConfigPath() != filepath.Join(claude, ".claude.json") {
		t.Fatalf("roots = %q %q %q", claudeRoot(), codexRoot(), claudeGlobalConfigPath())
	}
	// Transcripts under the relocated dir are what learn scans.
	writeClaudeProject(t, claude, "repo", "a.jsonl", []string{
		`{"type":"assistant","cwd":"/r","timestamp":"2026-09-20T10:00:00Z","message":{"id":"a","model":"claude-opus-5-5","usage":{"input_tokens":1000}}}`,
	})
	if got := scanLearnSessionMetrics(map[string]bool{"claude": true}, time.Time{}, "", false); len(got) != 1 {
		t.Fatalf("relocated transcripts not scanned: %+v", got)
	}
	// The explicit test override still wins.
	t.Setenv("CAVEMAN_CLAUDE_ROOT", filepath.Join(base, "override"))
	if claudeRoot() != filepath.Join(base, "override") {
		t.Fatalf("CAVEMAN_CLAUDE_ROOT must win: %q", claudeRoot())
	}
}

// Gemini CLI and opencode relocate their roots the same way Claude Code and
// Codex do, and the TypeScript CLI already honors both (GEMINI_CLI_HOME at
// packages/cli/src/index.ts, XDG_DATA_HOME for the kilo data root). The Go
// store did not, so `caveman learn` scanned $HOME paths the agents had not
// written to and reported zero sessions (#1081).
//
// Verified against the real binaries: gemini 0.62.0 with GEMINI_CLI_HOME set
// creates $GEMINI_CLI_HOME/.gemini and leaves $HOME alone; opencode 1.18.34
// with XDG_DATA_HOME set creates $XDG_DATA_HOME/opencode and leaves
// ~/.local/share alone.
func TestGeminiAndOpencodeRootsHonorRelocationVariables(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CAVEMAN_GEMINI_ROOT", "")
	t.Setenv("CAVEMAN_OPENCODE_ROOT", "")

	// Unset: both fall back to the documented $HOME defaults.
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	if got, want := geminiRoot(), filepath.Join(home, ".gemini"); got != want {
		t.Fatalf("geminiRoot() with no override = %q, want %q", got, want)
	}
	if got, want := opencodeRoot(), filepath.Join(home, ".local", "share", "opencode", "storage"); got != want {
		t.Fatalf("opencodeRoot() with no override = %q, want %q", got, want)
	}

	// GEMINI_CLI_HOME replaces the HOME directory, not the .gemini directory —
	// same semantics the TypeScript CLI implements.
	geminiHome := filepath.Join(base, "gemini-alt")
	t.Setenv("GEMINI_CLI_HOME", geminiHome)
	if got, want := geminiRoot(), filepath.Join(geminiHome, ".gemini"); got != want {
		t.Fatalf("geminiRoot() = %q, want %q", got, want)
	}

	// XDG_DATA_HOME replaces ~/.local/share, per the XDG base directory spec
	// opencode follows.
	dataHome := filepath.Join(base, "xdg-data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	if got, want := opencodeRoot(), filepath.Join(dataHome, "opencode", "storage"); got != want {
		t.Fatalf("opencodeRoot() = %q, want %q", got, want)
	}

	// The explicit CAVEMAN_* test overrides still win over both.
	t.Setenv("CAVEMAN_GEMINI_ROOT", filepath.Join(base, "g-override"))
	t.Setenv("CAVEMAN_OPENCODE_ROOT", filepath.Join(base, "o-override"))
	if geminiRoot() != filepath.Join(base, "g-override") {
		t.Fatalf("CAVEMAN_GEMINI_ROOT must win: %q", geminiRoot())
	}
	if opencodeRoot() != filepath.Join(base, "o-override") {
		t.Fatalf("CAVEMAN_OPENCODE_ROOT must win: %q", opencodeRoot())
	}
}
