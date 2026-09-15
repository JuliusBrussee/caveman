package store

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	enginetokens "github.com/JuliusBrussee/caveman/engine/tokens"
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
	Snapshots        []ConfigSnapshot
	ClaudeMDUser     *ConfigSnapshot
	ClaudeMDProject  *ConfigSnapshot
	ClaudeMDProjects []*ConfigSnapshot
	CodexAgents      *ConfigSnapshot
	Skills           []skillInfo
	SkillDescTokens  int // per-turn skill catalog tax (name+description only)
	TokenBasis       string
	HookCount        int
	// PerTurnHooks labels hooks configured on events that fire every turn. They
	// are cache-churn CANDIDATES, not identified causes: config is visible here,
	// hook output is not.
	PerTurnHooks []string
	PluginCount  int
	MCPScopes    []mcpScopeScan
}

type mcpScopeScan struct {
	Scope   string
	Path    string
	Servers []string
	Present bool
}

// Agent roots resolve local transcript/config dirs and stay env-overridable so
// tests never read or write a user's real agent data.
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

func geminiRoot() string {
	if r := os.Getenv("CAVEMAN_GEMINI_ROOT"); r != "" {
		return r
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini")
}

func opencodeRoot() string {
	if r := os.Getenv("CAVEMAN_OPENCODE_ROOT"); r != "" {
		return r
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "storage")
}

// Aider histories are project-relative and have no safe global discovery root.
// Keep this source disabled unless the caller explicitly supplies a scan root.
func aiderRoot() string {
	return os.Getenv("CAVEMAN_AIDER_ROOT")
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

// configTokenCount uses the engine's offline o200k tokenizer when available.
// The byte estimate remains an explicit deterministic fallback; callers always
// persist the returned basis beside the count.
func configTokenCount(text string) (int, string) {
	return configTokenCountWith(enginetokens.Default(), text)
}

func configTokenCountWith(counter enginetokens.Counter, text string) (int, string) {
	if counter != nil && counter.Name() == "o200k_base" {
		return counter.Count([]byte(text)), "o200k"
	}
	return estimateTokens(text), "bytes4"
}

// scanConfig reads every config-tax source for the current working directory and
// the user's global agent config. cwd is where `caveman learn` was invoked.
func scanConfig(cwd string) configScan {
	now := time.Now().UTC().Format(time.RFC3339)
	_, tokenBasis := configTokenCount("")
	sc := configScan{TokenBasis: tokenBasis}
	add := func(snap *ConfigSnapshot) {
		if snap == nil {
			return
		}
		snap.ObservedAt = now
		snap.MetadataJSON = metadataWithTokenBasis(snap.MetadataJSON, tokenBasis)
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
		sc.HookCount, sc.PerTurnHooks = scanHooks(filepath.Join(croot, "settings.json"))
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
		for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
			if snap := readMarkdownConfig("project", filepath.Join(dir, "CLAUDE.md"), "claude_md"); snap != nil {
				sc.ClaudeMDProjects = append(sc.ClaudeMDProjects, snap)
				if sc.ClaudeMDProject == nil {
					sc.ClaudeMDProject = snap
				}
				add(snap)
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
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
	sc.MCPScopes = scanMCPConfigs(cwd, croot, claudeGlobalConfigPath())
	return sc
}

func claudeGlobalConfigPath() string {
	if path := os.Getenv("CAVEMAN_CLAUDE_GLOBAL_CONFIG"); path != "" {
		return path
	}
	if root := os.Getenv("CAVEMAN_CLAUDE_ROOT"); root != "" {
		// Tests and alternate Claude homes must not fall through to real user config.
		return filepath.Join(root, ".claude.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

func scanMCPConfigs(cwd, croot, globalPath string) []mcpScopeScan {
	sources := []mcpScopeScan{
		{Scope: "cwd_mcp_json", Path: filepath.Join(cwd, ".mcp.json")},
		{Scope: "claude_root_mcp_json", Path: filepath.Join(croot, ".mcp.json")},
		{Scope: "claude_global", Path: globalPath},
	}
	for i := range sources {
		if (sources[i].Scope == "cwd_mcp_json" && cwd == "") ||
			(sources[i].Scope == "claude_root_mcp_json" && croot == "") || sources[i].Path == "" {
			continue
		}
		raw, err := os.ReadFile(sources[i].Path)
		if err != nil {
			continue
		}
		sources[i].Present = true
		sources[i].Servers = parseMCPServerNames(raw, cwd, sources[i].Scope == "claude_global")
	}
	return sources
}

// parseMCPServerNames handles shapes observed locally: an object-valued
// mcpServers map at top level, plus object-valued projects[cwd].mcpServers in
// ~/.claude.json. Only server names are retained; commands, URLs, and env stay out.
func parseMCPServerNames(raw []byte, cwd string, includeProject bool) []string {
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	names := map[string]bool{}
	add := func(value any) {
		for name, config := range asMap(value) {
			if strings.TrimSpace(name) == "" || len(asMap(config)) == 0 {
				continue
			}
			names[name] = true
		}
	}
	add(object["mcpServers"])
	if includeProject && cwd != "" {
		projects := asMap(object["projects"])
		project := asMap(projects[cwd])
		if len(project) == 0 {
			cleaned := filepath.Clean(cwd)
			project = asMap(projects[cleaned])
		}
		add(project["mcpServers"])
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// configTaxPerTurn is the statically measured token tax loaded on every turn:
// CLAUDE.md (user + project) + the user skill catalog. Hooks/plugins are
// reported as counts, not folded into the token figure, because this scanner
// does not resolve their effective context contribution.
func (sc configScan) configTaxPerTurn() int {
	total := sc.SkillDescTokens
	if sc.ClaudeMDUser != nil {
		total += sc.ClaudeMDUser.Tokens
	}
	if len(sc.ClaudeMDProjects) > 0 {
		for _, snap := range sc.ClaudeMDProjects {
			total += snap.Tokens
		}
	} else if sc.ClaudeMDProject != nil {
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
	tokens, basis := configTokenCount(text)
	return &ConfigSnapshot{
		Scope:        scope,
		Path:         path,
		Kind:         kind,
		Lines:        strings.Count(text, "\n") + 1,
		Tokens:       tokens,
		MetadataJSON: compactMeta(map[string]any{"token_basis": basis}),
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
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		name, desc := readSkillFrontmatter(path)
		if name == "" {
			name = e.Name()
		}
		descTokens, _ := configTokenCount(name + " " + desc)
		out = append(out, skillInfo{
			Name:       name,
			Path:       path,
			DescTokens: descTokens,
		})
	}
	return out
}

func metadataWithTokenBasis(raw, basis string) string {
	meta := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &meta)
	}
	meta["token_basis"] = basis
	return compactMeta(meta)
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
	count, _ := scanHooks(settingsPath)
	return count
}

// perTurnHookEvents fire on every turn, so anything they print lands in the
// prompt on every turn. They are the first place to look when a prompt cache is
// being re-written repeatedly. SessionStart is excluded: it runs once.
var perTurnHookEvents = map[string]bool{
	"UserPromptSubmit": true,
	"PreToolUse":       true,
	"PostToolUse":      true,
	"PreCompact":       true,
	"Stop":             true,
	"SubagentStop":     true,
}

// scanHooks returns the total hook count plus the matcher/command labels of the
// hooks configured on PER-TURN events. The labels are candidates for a cache
// investigation, never an accusation: this function reads configuration, and
// cannot see what any hook actually printed.
func scanHooks(settingsPath string) (int, []string) {
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return 0, nil
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return 0, nil
	}
	hooks, ok := obj["hooks"].(map[string]any)
	if !ok {
		return 0, nil
	}
	count := 0
	var perTurn []string
	for event, v := range hooks {
		switch x := v.(type) {
		case []any:
			count += len(x)
			if perTurnHookEvents[event] {
				perTurn = append(perTurn, hookCommandLabels(event, x)...)
			}
		case map[string]any:
			count += len(x)
		}
	}
	sort.Strings(perTurn)
	return count, perTurn
}

// hookCommandLabels extracts "<event>: <command basename>" for each configured
// hook. The full command line is deliberately not kept: it can carry paths and
// arguments that are none of the report's business.
func hookCommandLabels(event string, entries []any) []string {
	var out []string
	for _, entry := range entries {
		group, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		inner, ok := group["hooks"].([]any)
		if !ok {
			continue
		}
		for _, rawHook := range inner {
			hook, ok := rawHook.(map[string]any)
			if !ok {
				continue
			}
			command := strings.TrimSpace(fmt.Sprint(hook["command"]))
			if command == "" || command == "<nil>" {
				continue
			}
			out = append(out, event+": "+hookCommandBasename(command))
		}
	}
	return out
}

// hookCommandBasename reduces a hook command to something namable without
// leaking the user's filesystem layout: the basename of its first token.
func hookCommandBasename(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "hook"
	}
	base := filepath.Base(fields[0])
	if base == "node" || base == "python" || base == "python3" || base == "sh" || base == "bash" {
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "-") {
				continue
			}
			return base + " " + filepath.Base(field)
		}
	}
	return base
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
	currentStmt, err := tx.Prepare(
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
	defer currentStmt.Close()
	historyStmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO config_snapshot_history
		  (scope, path, kind, lines, tokens, observed_at, metadata_json)
		  VALUES (?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return 0, err
	}
	defer historyStmt.Close()
	n := 0
	for _, snap := range snaps {
		if snap.Path == "" {
			continue
		}
		if snap.ObservedAt == "" {
			snap.ObservedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if _, err := currentStmt.Exec(snap.Scope, snap.Path, snap.Kind, snap.Lines, snap.Tokens, snap.ObservedAt, snap.MetadataJSON); err != nil {
			return n, err
		}
		var latestLines, latestTokens int
		historyErr := tx.QueryRow(
			`SELECT lines, tokens FROM config_snapshot_history
			  WHERE scope = ? AND path = ? AND kind = ?
			  ORDER BY observed_at DESC, id DESC LIMIT 1`,
			snap.Scope, snap.Path, snap.Kind,
		).Scan(&latestLines, &latestTokens)
		if historyErr != nil && historyErr != sql.ErrNoRows {
			return n, historyErr
		}
		if historyErr == sql.ErrNoRows || latestLines != snap.Lines || latestTokens != snap.Tokens {
			if _, err := historyStmt.Exec(snap.Scope, snap.Path, snap.Kind, snap.Lines, snap.Tokens, snap.ObservedAt, snap.MetadataJSON); err != nil {
				return n, err
			}
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return n, err
	}
	return n, nil
}
