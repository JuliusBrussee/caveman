package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// config_scan.go measures the local agent config that loads into context every
// turn — the "config tax". It reads only local files (CLAUDE.md / AGENTS.md, skill
// descriptions, hooks, plugins) and never writes them. Token counts are an
// `inferred` estimate.

type skillInfo struct {
	Name       string
	Path       string
	DescTokens int
}

// configScan is the measured config-tax picture used by the detectors.
type configScan struct {
	Snapshots       []ConfigSnapshot
	ClaudeMDUser    *ConfigSnapshot
	ClaudeMDProject *ConfigSnapshot
	CodexAgents     *ConfigSnapshot
	Skills          []skillInfo
	SkillDescTokens int // per-turn skill catalog tax (name+description only)
	HookCount       int
	PluginCount     int
}

// claudeRoot / codexRoot resolve the agent config dirs, env-overridable for tests.
func claudeRoot() string {
	if r := os.Getenv("CAVEMAN_CLAUDE_ROOT"); r != "" {
		return r
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

func codexRoot() string {
	if r := os.Getenv("CAVEMAN_CODEX_ROOT"); r != "" {
		return r
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// estimateTokens is the project's inferred token heuristic (bytes/4), matching the
// usage importer's roughJSONSize/4 convention. Conservative, deterministic, no deps.
func estimateTokens(text string) int {
	n := len(text)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

// scanConfig reads every config-tax source for the current working directory and
// the user's global agent config. cwd is where `caveman learn` was invoked.
func scanConfig(cwd string) configScan {
	now := time.Now().UTC().Format(time.RFC3339)
	var sc configScan
	add := func(snap *ConfigSnapshot) {
		if snap == nil {
			return
		}
		snap.ObservedAt = now
		sc.Snapshots = append(sc.Snapshots, *snap)
	}

	croot := claudeRoot()
	if croot != "" {
		sc.ClaudeMDUser = readMarkdownConfig("user", filepath.Join(croot, "CLAUDE.md"), "claude_md")
		add(sc.ClaudeMDUser)
		sc.Skills = scanSkills(filepath.Join(croot, "skills"))
		for _, sk := range sc.Skills {
			sc.SkillDescTokens += sk.DescTokens
		}
		if len(sc.Skills) > 0 {
			add(&ConfigSnapshot{Scope: "skills", Path: filepath.Join(croot, "skills"), Kind: "skill_desc",
				Lines: len(sc.Skills), Tokens: sc.SkillDescTokens,
				MetadataJSON: compactMeta(map[string]any{"skill_count": len(sc.Skills)})})
		}
		sc.HookCount = countHooks(filepath.Join(croot, "settings.json"))
		if sc.HookCount > 0 {
			add(&ConfigSnapshot{Scope: "hooks", Path: filepath.Join(croot, "settings.json"), Kind: "hooks",
				Lines: sc.HookCount, Tokens: 0,
				MetadataJSON: compactMeta(map[string]any{"hook_count": sc.HookCount})})
		}
		sc.PluginCount = countPlugins(filepath.Join(croot, "plugins", "installed_plugins.json"))
		if sc.PluginCount > 0 {
			add(&ConfigSnapshot{Scope: "plugins", Path: filepath.Join(croot, "plugins", "installed_plugins.json"), Kind: "plugins",
				Lines: sc.PluginCount, Tokens: 0,
				MetadataJSON: compactMeta(map[string]any{"plugin_count": sc.PluginCount})})
		}
	}

	if cwd != "" {
		sc.ClaudeMDProject = readMarkdownConfig("project", filepath.Join(cwd, "CLAUDE.md"), "claude_md")
		add(sc.ClaudeMDProject)
	}

	croot2 := codexRoot()
	if croot2 != "" {
		sc.CodexAgents = readMarkdownConfig("user", filepath.Join(croot2, "AGENTS.md"), "agents_md")
		add(sc.CodexAgents)
	}
	if cwd != "" {
		if proj := readMarkdownConfig("project", filepath.Join(cwd, "AGENTS.md"), "agents_md"); proj != nil {
			add(proj)
		}
	}
	return sc
}

// configTaxPerTurn is the token tax loaded on every turn: CLAUDE.md (user +
// project) + the skill catalog. Hooks/plugins are reported as counts, not folded
// into the token figure, because their context cost can't be measured statically.
func (sc configScan) configTaxPerTurn() int {
	total := sc.SkillDescTokens
	if sc.ClaudeMDUser != nil {
		total += sc.ClaudeMDUser.Tokens
	}
	if sc.ClaudeMDProject != nil {
		total += sc.ClaudeMDProject.Tokens
	}
	return total
}

func readMarkdownConfig(scope, path, kind string) *ConfigSnapshot {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := string(raw)
	return &ConfigSnapshot{
		Scope:  scope,
		Path:   path,
		Kind:   kind,
		Lines:  strings.Count(text, "\n") + 1,
		Tokens: estimateTokens(text),
	}
}

// scanSkills reads <root>/*/SKILL.md and estimates each skill's per-turn catalog
// tax (its name + description frontmatter, which loads at session start — the body
// loads only on invocation).
func scanSkills(skillsDir string) []skillInfo {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil
	}
	var out []skillInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		name, desc := readSkillFrontmatter(path)
		if name == "" {
			name = e.Name()
		}
		out = append(out, skillInfo{
			Name:       name,
			Path:       path,
			DescTokens: estimateTokens(name + " " + desc),
		})
	}
	return out
}

func readSkillFrontmatter(path string) (name, desc string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	inFront := false
	started := false
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if !started {
				started = true
				inFront = true
				continue
			}
			break // end of frontmatter
		}
		if !inFront {
			if !started {
				// No frontmatter fence on the first non-empty line; bail.
				if trimmed != "" {
					return "", ""
				}
				continue
			}
			continue
		}
		if strings.HasPrefix(trimmed, "name:") {
			name = strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
		} else if strings.HasPrefix(trimmed, "description:") {
			desc = strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
		}
	}
	return strings.Trim(name, `"'`), strings.Trim(desc, `"'`)
}

func countHooks(settingsPath string) int {
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return 0
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return 0
	}
	hooks, ok := obj["hooks"].(map[string]any)
	if !ok {
		return 0
	}
	count := 0
	for _, v := range hooks {
		switch x := v.(type) {
		case []any:
			count += len(x)
		case map[string]any:
			count += len(x)
		}
	}
	return count
}

func countPlugins(pluginsPath string) int {
	raw, err := os.ReadFile(pluginsPath)
	if err != nil {
		return 0
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return 0
	}
	switch x := v.(type) {
	case []any:
		return len(x)
	case map[string]any:
		// Common shape: { "<marketplace>": ["plugin@ver", ...] } or { plugins: [...] }
		if arr, ok := x["plugins"].([]any); ok {
			return len(arr)
		}
		total := 0
		counted := false
		for _, child := range x {
			if arr, ok := child.([]any); ok {
				total += len(arr)
				counted = true
			}
		}
		if counted {
			return total
		}
		return len(x)
	}
	return 0
}

// InsertConfigSnapshots persists the measured config sources (evidence/export).
func (s *Store) InsertConfigSnapshots(snaps []ConfigSnapshot) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(
		`INSERT INTO config_snapshots (scope, path, kind, lines, tokens, observed_at, metadata_json)
		  VALUES (?, ?, ?, ?, ?, ?, ?)
		  ON CONFLICT(scope, path, kind) DO UPDATE SET
		    lines = excluded.lines,
		    tokens = excluded.tokens,
		    observed_at = excluded.observed_at,
		    metadata_json = excluded.metadata_json`,
	)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	n := 0
	for _, snap := range snaps {
		if snap.Path == "" {
			continue
		}
		if snap.ObservedAt == "" {
			snap.ObservedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if _, err := stmt.Exec(snap.Scope, snap.Path, snap.Kind, snap.Lines, snap.Tokens, snap.ObservedAt, snap.MetadataJSON); err != nil {
			return n, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return n, err
	}
	return n, nil
}
