package store

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// detect_memory_health.go is the memory & rules doctor: deterministic, read-only
// audits of the instruction and memory files an agent loads at session start
// (CLAUDE.md / CLAUDE.local.md / .claude/rules, AGENTS.md, GEMINI.md, and Claude
// Code auto memory). Every sink id is `memory_health:<kind>:<scope-or-fingerprint>`.

const (
	// Claude Code docs, https://code.claude.com/docs/en/memory (checked
	// 2026-09-27): "The first 200 lines of MEMORY.md, or the first 25KB,
	// whichever comes first, are loaded at the start of every conversation.
	// Content beyond that threshold is not loaded at session start."
	memoryIndexLineLimit = 200
	memoryIndexByteLimit = 25 * 1024

	memoryHealthMaxFileBytes = 512 << 10 // skip anything larger: not a hand-written rules file
	memoryHealthMaxDirFiles  = 500       // bound the memory-dir and rules-dir walks
	buriedRulesMinFileLines  = 150
	buriedRulesMinCount      = 3
	duplicateRuleMinWords    = 6
	memoryHealthSampleCap    = 20
)

var (
	mhFence    = regexp.MustCompile("^\\s*(```|~~~)")
	mhCodeSpan = regexp.MustCompile("`[^`]*`")
	mhBacktick = regexp.MustCompile("`([^`\\s]+)`")
	mhImport   = regexp.MustCompile(`(?:^|\s)@(\S+)`)
	mhMDLink   = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	mhExt      = regexp.MustCompile(`\.[A-Za-z0-9]{1,8}$`)
	// mhImportExt is the conservative set of text files an extension-only
	// @import may name; "@v1.2" or "@john.doe" must not read as imports.
	mhImportExt   = regexp.MustCompile(`(?i)\.(md|mdx|markdown|txt|json|jsonc|yaml|yml|toml)$`)
	mhLineSuffix  = regexp.MustCompile(`(:\d+(:\d+)?|#L\d+(-L?\d+)?)$`)
	mhBullet      = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+`)
	mhEmphatic    = regexp.MustCompile(`\b(NEVER|ALWAYS|MUST|IMPORTANT|CRITICAL)\b`)
	mhPathReject  = regexp.MustCompile(`[<>{}*?$\[\]|()@=,;"'\\]`)
	mhEntryPrefix = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+`)
	mhPlaceholder = regexp.MustCompile(`\b(NN+|XX+|YYYY|MM|DD)\b|(?i)\b(foo|bar|baz|example|placeholder|(?:my|your)[-_]\w+)\b`)
	// ponytail: English-only removal cue list; lines in other languages are not skipped.
	mhRemovalWords = regexp.MustCompile(`(?i)\b(removed?|deleted?|gone|no longer|legacy|formerly|used to|renamed|deprecated|old|historical|leftovers?|stale|not yet|planned|will be|future)\b`)
)

type instructionFile struct {
	Path    string
	Scope   string          // user | project | memory
	Agents  map[string]bool // which agents load it at session start
	Lines   []string
	Loaded  int // lines actually loaded at session start (MEMORY.md cutoff), else len(Lines)
	RepoTop string
}

func memoryHealthSinks(cwd string, turnsPerDay float64) []Sink {
	files, memDir := collectInstructionFiles(cwd)
	var sinks []Sink
	var memIndex *instructionFile
	for i := range files {
		if files[i].Scope == "memory" {
			memIndex = &files[i]
		}
	}
	// Repo-specific findings name their repo so a nudge can say where they are;
	// the memory sinks' ids also carry a memory-dir fingerprint, so repo B's
	// truncation is never mistaken for repo A's already-announced one.
	withRepo := func(found []Sink, repo string) []Sink {
		if repo != "" && repo != "." && repo != string(filepath.Separator) {
			for i := range found {
				found[i].Evidence["repo"] = filepath.Base(repo)
			}
		}
		return found
	}
	if memIndex != nil {
		repo := ""
		if cwd != "" {
			repo = memoryProjectRoot(cwd)
		}
		sinks = append(sinks, withRepo(memoryTruncationSink(memIndex), repo)...)
		sinks = append(sinks, withRepo(memoryOrphansSink(memIndex, memDir, turnsPerDay), repo)...)
	}
	for i := range files {
		f := &files[i]
		if f.Scope == "memory" {
			continue
		}
		var found []Sink
		if f.Agents["claude"] || f.Agents["gemini"] {
			// @path imports are Claude Code and Gemini CLI syntax; Codex reads @ literally.
			found = append(found, brokenImportsSink(f)...)
		}
		if f.Scope == "project" {
			found = append(found, staleReferencesSink(f)...)
		}
		found = append(found, buriedRulesSink(f)...)
		if f.Scope == "project" {
			found = withRepo(found, f.RepoTop)
		}
		sinks = append(sinks, found...)
	}
	for _, agent := range []string{"claude", "codex", "gemini"} {
		sinks = append(sinks, duplicateRulesSink(files, agent, turnsPerDay)...)
	}
	return sinks
}

// repoTop is the nearest ancestor holding a .git entry (dir or worktree file).
func repoTop(dir string) string {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// memoryProjectRoot is the directory Claude Code keys auto memory on: the git
// repository, with every worktree folded into its main checkout (per the docs,
// "all worktrees and subdirectories within the same repo share one auto memory
// directory"). Outside a repo it is cwd.
func memoryProjectRoot(cwd string) string {
	top := repoTop(cwd)
	if top == "" {
		return filepath.Clean(cwd)
	}
	raw, err := os.ReadFile(filepath.Join(top, ".git"))
	if err != nil {
		return top // .git is a directory: main checkout
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "gitdir:"))
	if i := strings.Index(filepath.ToSlash(gitdir), "/.git/worktrees/"); i > 0 {
		return filepath.FromSlash(filepath.ToSlash(gitdir)[:i])
	}
	return top
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

func autoMemoryDir(cwd string) string {
	root := claudeRoot()
	if root == "" || cwd == "" {
		return ""
	}
	name := os.Getenv("CLAUDE_CODE_PROJECT_DIR_NAME")
	if name == "" {
		name = nonAlnum.ReplaceAllString(memoryProjectRoot(cwd), "-")
	}
	// ponytail: autoMemoryDirectory in settings.json is not resolved; add when a user relies on it.
	return filepath.Join(root, "projects", name, "memory")
}

func readInstructionLines(path string) ([]string, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > memoryHealthMaxFileBytes {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n"), true
}

func isPathScopedRule(lines []string) bool {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return false
	}
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "---" {
			return false
		}
		if strings.HasPrefix(strings.TrimSpace(l), "paths:") {
			return true
		}
	}
	return false
}

func collectInstructionFiles(cwd string) ([]instructionFile, string) {
	var files []instructionFile
	index := map[string]int{}
	add := func(path, scope, agent string, rule bool) {
		key := path
		if real, err := filepath.EvalSymlinks(path); err == nil {
			key = real
		}
		if i, ok := index[key]; ok {
			files[i].Agents[agent] = true
			return
		}
		lines, ok := readInstructionLines(path)
		if !ok || (rule && isPathScopedRule(lines)) {
			return // path-scoped rules load on demand, not at session start
		}
		index[key] = len(files)
		files = append(files, instructionFile{Path: path, Scope: scope, Agents: map[string]bool{agent: true},
			Lines: lines, Loaded: len(lines), RepoTop: repoTop(filepath.Dir(path))})
	}
	addRules := func(dir, scope string) {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		if len(matches) > memoryHealthMaxDirFiles {
			matches = matches[:memoryHealthMaxDirFiles]
		}
		for _, m := range matches {
			add(m, scope, "claude", true)
		}
	}

	if croot := claudeRoot(); croot != "" {
		add(filepath.Join(croot, "CLAUDE.md"), "user", "claude", false)
		addRules(filepath.Join(croot, "rules"), "user")
	}
	var chain []string
	top := ""
	if cwd != "" {
		top = repoTop(cwd)
		for d := filepath.Clean(cwd); ; d = filepath.Dir(d) {
			chain = append(chain, d)
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	claudeProject := false
	for _, d := range chain {
		before := len(files)
		add(filepath.Join(d, "CLAUDE.md"), "project", "claude", false)
		add(filepath.Join(d, ".claude", "CLAUDE.md"), "project", "claude", false)
		add(filepath.Join(d, "CLAUDE.local.md"), "project", "claude", false)
		claudeProject = claudeProject || len(files) > before
		addRules(filepath.Join(d, ".claude", "rules"), "project")
	}
	if r := codexRoot(); r != "" {
		add(filepath.Join(r, "AGENTS.md"), "user", "codex", false)
	}
	if r := geminiRoot(); r != "" {
		add(filepath.Join(r, "GEMINI.md"), "user", "gemini", false)
	}
	for _, d := range chain {
		// Codex and Gemini walk from the repo root down to cwd; Claude reads
		// AGENTS.md only when no project CLAUDE.md/CLAUDE.local.md exists.
		if top == "" || strings.HasPrefix(d, top) {
			add(filepath.Join(d, "AGENTS.md"), "project", "codex", false)
			add(filepath.Join(d, "GEMINI.md"), "project", "gemini", false)
		}
		if !claudeProject {
			add(filepath.Join(d, "AGENTS.md"), "project", "claude", false)
		}
	}

	memDir := autoMemoryDir(cwd)
	if memDir != "" {
		idx := filepath.Join(memDir, "MEMORY.md")
		if lines, ok := readInstructionLines(idx); ok {
			files = append(files, instructionFile{Path: idx, Scope: "memory", Agents: map[string]bool{"claude": true},
				Lines: lines, Loaded: memoryIndexCutoff(lines)})
		}
	}
	return files, memDir
}

// memoryIndexCutoff is how many leading lines of MEMORY.md load at session start.
func memoryIndexCutoff(lines []string) int {
	bytes := 0
	for i, l := range lines {
		if i >= memoryIndexLineLimit {
			return i
		}
		bytes += len(l) + 1
		if bytes > memoryIndexByteLimit {
			return i
		}
	}
	return len(lines)
}

// proseLines reports, per line, whether it is prose: outside code fences and a
// leading YAML frontmatter block.
func proseLines(lines []string) []bool {
	out := make([]bool, len(lines))
	fenced, front := false, len(lines) > 0 && strings.TrimSpace(lines[0]) == "---"
	for i, l := range lines {
		if front {
			if i > 0 && strings.TrimSpace(l) == "---" {
				front = false
			}
			continue
		}
		if mhFence.MatchString(l) {
			fenced = !fenced
			continue
		}
		out[i] = !fenced
	}
	return out
}

func fileFingerprint(path string) string { return hashText(path)[:8] }

// pick returns one or many by count, for titles like "1 rule loads".
func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// memoryOrphansTitle names only the problems present: "3 memory files are
// not linked from MEMORY.md", "and 2 links point to missing files".
func memoryOrphansTitle(orphans, dangling int) string {
	var parts []string
	if orphans > 0 {
		parts = append(parts, fmt.Sprintf("%s %s not linked from MEMORY.md", plural(orphans, "memory file"), pick(orphans, "is", "are")))
	}
	if dangling > 0 {
		parts = append(parts, fmt.Sprintf("%s in MEMORY.md %s to missing files", plural(dangling, "link"), pick(dangling, "points", "point")))
	}
	return strings.Join(parts, ", and ")
}

func memorySink(kind, scope, title, class string, tokensPerTurn int, turnsPerDay float64, evidence map[string]any, suggestion string) Sink {
	s := Sink{
		SinkID: "memory_health:" + kind + ":" + scope, Title: title, Class: class, Basis: learnBasis,
		Framing: framingForward, Evidence: evidence, Suggestion: suggestion,
	}
	if tokensPerTurn > 0 {
		s.TokensPerTurn = int64(tokensPerTurn)
		s.TokensPerDayRate = rate(tokensPerTurn, turnsPerDay)
	}
	return s
}

func memoryTruncationSink(f *instructionFile) []Sink {
	past := len(f.Lines) - f.Loaded
	if past <= 0 {
		return nil
	}
	entries := 0
	for _, l := range f.Lines[f.Loaded:] {
		if mhEntryPrefix.MatchString(l) {
			entries++
		}
	}
	notLoaded, basis := configTokenCount(strings.Join(f.Lines[f.Loaded:], "\n"))
	return []Sink{memorySink("memory_truncation", fileFingerprint(filepath.Dir(f.Path)),
		fmt.Sprintf("MEMORY.md is %s long; the last %s (%d %s) never %s, because only the start is read",
			plural(len(f.Lines), "line"), plural(past, "line"), entries, pick(entries, "memory entry", "memory entries"), pick(past, "loads", "load")),
		classBehavioral, 0, 0, map[string]any{
			"path": f.Path, "lines": len(f.Lines), "loaded_lines": f.Loaded, "lines_past_cutoff": past,
			"entries_past_cutoff": entries, "tokens_not_loaded": notLoaded, "token_basis": basis,
			"limit":    fmt.Sprintf("first %d lines or %d bytes, whichever comes first", memoryIndexLineLimit, memoryIndexByteLimit),
			"fix_kind": "memory_index_condense",
		},
		"Claude Code only reads the start of MEMORY.md in each session (see its docs). Consider shortening it to one line per memory and moving the details into the linked files, so the entries at the end are read again.")}
}

func memoryOrphansSink(f *instructionFile, memDir string, turnsPerDay float64) []Sink {
	text := strings.Join(f.Lines, "\n")
	var dangling []string
	danglingTokens := 0
	for i, l := range f.Lines {
		for _, m := range mhMDLink.FindAllStringSubmatch(l, -1) {
			target := strings.SplitN(m[1], "#", 2)[0]
			if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			p := target
			if !filepath.IsAbs(p) {
				p = filepath.Join(memDir, p)
			}
			if _, err := os.Stat(p); err != nil {
				dangling = append(dangling, fmt.Sprintf("MEMORY.md:%d -> %s", i+1, target))
				if i < f.Loaded {
					n, _ := configTokenCount(l)
					danglingTokens += n
				}
			}
		}
	}
	var orphans []string
	walked := 0
	_ = filepath.WalkDir(memDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || walked >= memoryHealthMaxDirFiles {
			return filepath.SkipAll
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") || d.Name() == "MEMORY.md" {
			return nil
		}
		walked++
		rel, _ := filepath.Rel(memDir, path)
		// Any mention of the file name counts as linked: conservative on purpose.
		if !strings.Contains(text, d.Name()) {
			orphans = append(orphans, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(orphans) == 0 && len(dangling) == 0 {
		return nil
	}
	sort.Strings(orphans)
	class := classBehavioral
	if danglingTokens > 0 {
		class = classReducible
	}
	_, basis := configTokenCount("")
	return []Sink{memorySink("memory_orphans", fileFingerprint(memDir),
		memoryOrphansTitle(len(orphans), len(dangling)),
		class, danglingTokens, turnsPerDay, map[string]any{
			"path": f.Path, "memory_dir": memDir, "orphan_files": capStrings(orphans), "orphan_count": len(orphans),
			"dangling_links": capStrings(dangling), "dangling_count": len(dangling),
			"dangling_tokens_per_turn": danglingTokens, "token_basis": basis, "fix_kind": "memory_index_repair",
		},
		"Unlinked files are never listed in the MEMORY.md index, so the agent isn't pointed at them. Broken links load in every session and lead nowhere. Consider linking or deleting the unlinked files and fixing or removing the broken links, with the user's yes for each.")}
}

func brokenImportsSink(f *instructionFile) []Sink {
	home, _ := os.UserHomeDir()
	prose := proseLines(f.Lines)
	var broken []string
	for i, l := range f.Lines {
		if !prose[i] {
			continue
		}
		for _, m := range mhImport.FindAllStringSubmatch(mhCodeSpan.ReplaceAllString(l, " "), -1) {
			tok := strings.TrimRight(m[1], ".,;:!?)\"'")
			if !importLooksLikePath(tok) {
				continue
			}
			p := tok
			switch {
			case strings.HasPrefix(p, "~/"):
				if home == "" {
					continue
				}
				p = filepath.Join(home, p[2:])
			case !filepath.IsAbs(p):
				p = filepath.Join(filepath.Dir(f.Path), p)
			}
			if _, err := os.Stat(p); err != nil {
				broken = append(broken, fmt.Sprintf("%d: @%s", i+1, tok))
			}
		}
	}
	if len(broken) == 0 {
		return nil
	}
	return []Sink{memorySink("broken_imports", fileFingerprint(f.Path),
		fmt.Sprintf("%s (%s) has %s", filepath.Base(f.Path), f.Scope, pick(len(broken), "1 @import that points to a missing file", fmt.Sprintf("%d @imports that point to missing files", len(broken)))),
		classBehavioral, 0, 0, map[string]any{"path": f.Path, "imports": capStrings(broken), "count": len(broken)},
		"These @imports load nothing, so whatever they should pull in is quietly missing. Consider fixing each path or removing the import.")}
}

// importLooksLikePath keeps only tokens that are unambiguously file imports:
// an explicit ./ ../ ~/ / prefix or a known text-file extension. @mentions,
// versions, npm scopes and emails fall out. A //host path is refused so the
// doctor never stats a UNC share (an SMB round trip on Windows).
func importLooksLikePath(tok string) bool {
	if tok == "" || strings.HasPrefix(tok, "//") || strings.Contains(tok, "://") || strings.Contains(tok, "@") || mhPathReject.MatchString(tok) {
		return false
	}
	if strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "../") || strings.HasPrefix(tok, "~/") || strings.HasPrefix(tok, "/") {
		return true
	}
	return mhImportExt.MatchString(filepath.Base(tok))
}

func staleReferencesSink(f *instructionFile) []Sink {
	root := f.RepoTop
	if root == "" {
		return nil // no repo, no repo-relative paths to check
	}
	prose := proseLines(f.Lines)
	seen := map[string]bool{}
	var stale []string
	for i, l := range f.Lines {
		if !prose[i] {
			continue
		}
		if mhRemovalWords.MatchString(mhCodeSpan.ReplaceAllString(l, " ")) {
			continue // "we removed `x/`" names a stale path on purpose
		}
		for _, m := range mhBacktick.FindAllStringSubmatch(l, -1) {
			ref, ok := repoRelativeRef(m[1])
			if !ok || seen[ref] {
				continue
			}
			seen[ref] = true
			first := strings.SplitN(ref, "/", 2)[0]
			// Anchor: only a path whose top-level entry exists in this repo is a
			// claim about this repo; `.caveman/config.json` or a removed `.cursor/`
			// are not.
			if _, err := os.Stat(filepath.Join(root, first)); err != nil {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ref))); err != nil {
				stale = append(stale, fmt.Sprintf("%d: %s", i+1, ref))
			}
		}
	}
	if len(stale) == 0 {
		return nil
	}
	return []Sink{memorySink("stale_references", fileFingerprint(f.Path),
		fmt.Sprintf("%s names %s", filepath.Base(f.Path), pick(len(stale), "1 repo path that no longer exists", fmt.Sprintf("%d repo paths that no longer exist", len(stale)))),
		classBehavioral, 0, 0, map[string]any{"path": f.Path, "repo_root": root, "references": capStrings(stale), "count": len(stale)},
		"Instructions that point at moved or deleted files can send the agent to the wrong place. Consider updating or removing these paths.")}
}

func repoRelativeRef(tok string) (string, bool) {
	// Dot-dirs (`.github/copilot-instructions.md`, `.cursor/rules/`) are usually
	// files an installer writes into OTHER repos, not claims about this one.
	if strings.Contains(tok, "://") || mhPathReject.MatchString(tok) || mhPlaceholder.MatchString(tok) ||
		strings.HasPrefix(tok, "~") || strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "-") ||
		strings.HasPrefix(strings.TrimPrefix(tok, "./"), ".") {
		return "", false
	}
	tok = mhLineSuffix.ReplaceAllString(strings.TrimPrefix(tok, "./"), "")
	if !strings.Contains(tok, "/") || strings.Contains(tok, "..") || strings.Contains(tok, "//") {
		return "", false
	}
	if !strings.HasSuffix(tok, "/") && !mhExt.MatchString(tok[strings.LastIndex(tok, "/")+1:]) {
		return "", false
	}
	return tok, true
}

func buriedRulesSink(f *instructionFile) []Sink {
	n := len(f.Lines)
	if n <= buriedRulesMinFileLines {
		return nil
	}
	prose := proseLines(f.Lines)
	lo, hi := n*30/100, n*70/100
	var lines []int
	for i := lo; i < hi; i++ {
		if prose[i] && mhEmphatic.MatchString(f.Lines[i]) {
			lines = append(lines, i+1)
		}
	}
	if len(lines) < buriedRulesMinCount {
		return nil
	}
	sample := lines
	if len(sample) > memoryHealthSampleCap {
		sample = sample[:memoryHealthSampleCap]
	}
	return []Sink{memorySink("buried_rules", fileFingerprint(f.Path),
		fmt.Sprintf("%s: %s in the middle of a %d-line file", filepath.Base(f.Path), pick(len(lines), "1 important rule (IMPORTANT, MUST, NEVER…) is buried", fmt.Sprintf("%d important rules (IMPORTANT, MUST, NEVER…) are buried", len(lines))), n),
		classBehavioral, 0, 0, map[string]any{"path": f.Path, "lines": n, "count": len(lines), "rule_lines": sample,
			"band": "30-70% of the file", "method": "heuristic"},
		"Rule of thumb, not a measurement: models tend to pay more attention to the start and end of a long file. Consider moving these rules up.")}
}

type dupRule struct {
	text   string
	tokens int
	locs   []string
	paths  map[string]bool
}

func duplicateRulesSink(files []instructionFile, agent string, turnsPerDay float64) []Sink {
	rules := map[string]*dupRule{}
	var order []string
	for _, f := range files {
		if !f.Agents[agent] {
			continue
		}
		prose := proseLines(f.Lines)
		for i := 0; i < f.Loaded; i++ {
			if !prose[i] {
				continue
			}
			raw := strings.TrimSpace(f.Lines[i])
			if raw == "" || strings.HasPrefix(raw, "#") {
				continue
			}
			norm := normalizeRule(raw)
			if len(strings.Fields(norm)) < duplicateRuleMinWords {
				continue
			}
			r := rules[norm]
			if r == nil {
				tokens, _ := configTokenCount(raw)
				r = &dupRule{text: raw, tokens: tokens, paths: map[string]bool{}}
				rules[norm] = r
				order = append(order, norm)
			}
			if !r.paths[f.Path] {
				r.paths[f.Path] = true
				r.locs = append(r.locs, fmt.Sprintf("%s:%d", f.Path, i+1))
			}
		}
	}
	type dupEvidence struct {
		Text      string   `json:"text"`
		Copies    int      `json:"copies"`
		Tokens    int      `json:"tokens_per_copy"`
		Locations []string `json:"locations"`
	}
	var dups []dupEvidence
	tokens := 0
	for _, norm := range order {
		r := rules[norm]
		if len(r.locs) < 2 {
			continue
		}
		tokens += (len(r.locs) - 1) * r.tokens
		text := r.text
		if r := []rune(text); len(r) > 160 {
			text = string(r[:160]) + "…"
		}
		dups = append(dups, dupEvidence{Text: text, Copies: len(r.locs), Tokens: r.tokens, Locations: r.locs})
	}
	if len(dups) == 0 || tokens == 0 {
		return nil
	}
	total := len(dups)
	if len(dups) > memoryHealthSampleCap {
		dups = dups[:memoryHealthSampleCap]
	}
	_, basis := configTokenCount("")
	return []Sink{memorySink("duplicate_rules", agent,
		fmt.Sprintf("%s more than once in each %s session (~%s extra tokens in every message)", pick(total, "1 rule loads", fmt.Sprintf("%d rules load", total)), sourceDisplayName(agent), commaInt(int64(tokens))),
		classReducible, tokens, turnsPerDay, map[string]any{
			"agent": agent, "duplicates": dups, "duplicate_count": total, "token_basis": basis,
			"fix_kind": "dedupe_rules",
		},
		"The same rule is loaded from more than one file with every message. Keep the copy in the most specific file that needs it and remove the others, one edit at a time, with the user's yes.")}
}

func normalizeRule(line string) string {
	line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), ">"))
	line = mhBullet.ReplaceAllString(line, "")
	line = strings.NewReplacer("**", "", "__", "", "`", "").Replace(line)
	return strings.TrimRight(normalizeEchoText(line), ".,;:!")
}

func capStrings(v []string) []string {
	if len(v) > memoryHealthSampleCap {
		return v[:memoryHealthSampleCap]
	}
	if v == nil {
		return []string{}
	}
	return v
}
