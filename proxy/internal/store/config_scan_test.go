package store

import (
	"path/filepath"
	"testing"
	"time"
)

// CAVEMAN_CLAUDE_ROOT is Caveman's own override and stays the strongest, so the
// existing suite keeps reading its fixtures instead of a real config dir.
func TestClaudeRootPrefersCavemanOverride(t *testing.T) {
	override := t.TempDir()
	t.Setenv("CAVEMAN_CLAUDE_ROOT", override)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	if got := claudeRoot(); got != override {
		t.Fatalf("claudeRoot() = %q, want the CAVEMAN_CLAUDE_ROOT override %q", got, override)
	}
}

// CLAUDE_CONFIG_DIR is what Claude Code itself honors. Ignoring it made learn
// report "0 sessions" for anyone whose config is not in ~/.claude, while the
// CLI, the installer, and INSTALL.md all read the variable.
func TestClaudeRootHonorsClaudeConfigDir(t *testing.T) {
	t.Setenv("CAVEMAN_CLAUDE_ROOT", "")
	override := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", override)
	// A populated home must not win: that is the bug.
	t.Setenv("HOME", t.TempDir())

	if got := claudeRoot(); got != override {
		t.Fatalf("claudeRoot() = %q, want the CLAUDE_CONFIG_DIR override %q", got, override)
	}
}

func TestClaudeRootDefaultsToHomeClaude(t *testing.T) {
	t.Setenv("CAVEMAN_CLAUDE_ROOT", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got, want := claudeRoot(), filepath.Join(home, ".claude"); got != want {
		t.Fatalf("claudeRoot() = %q, want %q", got, want)
	}
}

// End-to-end for the reported symptom: a learn session source must read
// transcripts from CLAUDE_CONFIG_DIR, not from a home that has none.
func TestLearnSessionSourceReadsClaudeConfigDir(t *testing.T) {
	t.Setenv("CAVEMAN_CLAUDE_ROOT", "")
	override := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", override)
	t.Setenv("HOME", t.TempDir())

	writeClaudeProject(t, override, "-repo", "a.jsonl", []string{
		`{"type":"assistant","cwd":"/repo","timestamp":"2026-08-10T12:00:00Z","message":{"id":"m1","model":"claude-sonnet-4-6","usage":{"input_tokens":1000}}}`,
	})

	var claude sessionSource
	for _, source := range learnSessionSources() {
		if source.id() == "claude" {
			claude = source
		}
	}
	if claude == nil {
		t.Fatal("learnSessionSources() has no claude source")
	}

	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	if truncated := scanSessionSourceUntil(claude, time.Time{}, nil, &beh, newRecurringMiner(), nil); truncated {
		t.Fatal("fixture scan truncated")
	}
	if beh.SessionsBySource["claude"] != 1 {
		t.Fatalf("claude sessions = %d, want the 1 transcript under CLAUDE_CONFIG_DIR", beh.SessionsBySource["claude"])
	}
}

// The two sites below resolve a Claude root of their own instead of going
// through claudeRoot(), so honoring CLAUDE_CONFIG_DIR in claudeRoot() alone
// left them reading ~/.claude. Same reported symptom (#1124), different
// entrypoint: the MCP-tax scan reads the wrong .claude.json, and
// `caveman-proxy usage import claude` with no --path imports nothing.

// The CLI twin resolves .claude.json INSIDE the override
// (packages/cli/src/index.ts claudeJSONPath), so Go must agree or the MCP-tax
// scan reports "claude_global" absent for every CLAUDE_CONFIG_DIR user.
func TestClaudeGlobalConfigPathHonorsClaudeConfigDir(t *testing.T) {
	t.Setenv("CAVEMAN_CLAUDE_GLOBAL_CONFIG", "")
	t.Setenv("CAVEMAN_CLAUDE_ROOT", "")
	override := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", override)
	t.Setenv("HOME", t.TempDir())

	want := filepath.Join(override, ".claude.json")
	if got := claudeGlobalConfigPath(); got != want {
		t.Fatalf("claudeGlobalConfigPath() = %q, want %q", got, want)
	}
}

// Caveman's own override must stay the strongest signal here too.
func TestClaudeGlobalConfigPathPrefersExplicitOverrides(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "explicit.json")
	t.Setenv("CAVEMAN_CLAUDE_GLOBAL_CONFIG", explicit)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if got := claudeGlobalConfigPath(); got != explicit {
		t.Fatalf("claudeGlobalConfigPath() = %q, want the explicit override %q", got, explicit)
	}

	t.Setenv("CAVEMAN_CLAUDE_GLOBAL_CONFIG", "")
	root := t.TempDir()
	t.Setenv("CAVEMAN_CLAUDE_ROOT", root)
	if got, want := claudeGlobalConfigPath(), filepath.Join(root, ".claude.json"); got != want {
		t.Fatalf("claudeGlobalConfigPath() = %q, want the CAVEMAN_CLAUDE_ROOT path %q", got, want)
	}
}

// `caveman-proxy usage import claude` passes --path through verbatim and the
// flag defaults to "", so the empty-root fallback IS the shipped default.
func TestImportClaudeEmptyRootHonorsClaudeConfigDir(t *testing.T) {
	t.Setenv("CAVEMAN_CLAUDE_ROOT", "")
	override := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", override)
	t.Setenv("HOME", t.TempDir())

	writeClaudeProject(t, override, "-repo", "s.jsonl", []string{
		`{"type":"assistant","timestamp":"2026-08-10T12:00:00Z","message":{"id":"m1","model":"claude-sonnet-4-6","usage":{"input_tokens":2000,"output_tokens":500}}}`,
	})

	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	out, err := s.ImportClaude("", "")
	if err != nil {
		t.Fatalf("import claude: %v", err)
	}
	if out.EventsImported != 1 {
		t.Fatalf("EventsImported = %d, want the 1 event under CLAUDE_CONFIG_DIR", out.EventsImported)
	}
}
