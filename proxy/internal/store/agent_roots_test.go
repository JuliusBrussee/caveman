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
	t.Setenv("HOME", filepath.Join(base, "home"))
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
