package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memoryHealthEnv isolates every root the doctor reads so no test touches the
// real ~/.claude, ~/.codex or ~/.gemini. It returns (claudeRoot, repo).
func memoryHealthEnv(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	claude := filepath.Join(base, "claude")
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claude)
	t.Setenv("CAVEMAN_CODEX_ROOT", filepath.Join(base, "codex"))
	t.Setenv("CAVEMAN_GEMINI_ROOT", filepath.Join(base, "gemini"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CLAUDE_CODE_PROJECT_DIR_NAME", "")
	repo := filepath.Join(base, "repo")
	mustMkdir(t, filepath.Join(repo, ".git"))
	mustMkdir(t, filepath.Join(base, "home"))
	return claude, repo
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func memorySinksWithPrefix(sinks []Sink, prefix string) []Sink {
	var out []Sink
	for _, s := range sinks {
		if strings.HasPrefix(s.SinkID, prefix) {
			out = append(out, s)
		}
	}
	return out
}

func numberedLines(n int, format string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, format+"\n", i)
	}
	return b.String()
}

func TestMemoryTruncation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantHit bool
		past    int
	}{
		{"under limit", numberedLines(150, "- [entry %d](e.md) — note"), false, 0},
		{"over 200 lines", numberedLines(250, "- entry %d"), true, 50},
		{"over byte limit", numberedLines(100, "- entry %03d "+strings.Repeat("x", 300)), true, 100 - 81}, // 313-byte lines: 81 fit in 25600
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := memoryHealthEnv(t)
			mustWrite(t, filepath.Join(autoMemoryDir(repo), "MEMORY.md"), tc.body)
			got := memorySinksWithPrefix(memoryHealthSinks(repo, 0), "memory_health:memory_truncation:")
			if (len(got) == 1) != tc.wantHit {
				t.Fatalf("hit=%v want %v: %+v", len(got) == 1, tc.wantHit, got)
			}
			if tc.wantHit {
				if got[0].Evidence["lines_past_cutoff"] != tc.past || got[0].Evidence["entries_past_cutoff"] != tc.past {
					t.Fatalf("evidence = %+v", got[0].Evidence)
				}
				if got[0].Class != classBehavioral || got[0].TokensPerTurn != 0 {
					t.Fatalf("truncation must be behavioral with no per-turn claim: %+v", got[0])
				}
			}
		})
	}
}

func TestMemoryOrphans(t *testing.T) {
	for _, tc := range []struct {
		name         string
		index        string
		files        []string
		wantHit      bool
		wantClass    string
		wantOrphans  int
		wantDangling int
	}{
		{"all linked", "- [A](a.md)\n- see b.md too\n", []string{"a.md", "b.md"}, false, "", 0, 0},
		{"orphan only", "- [A](a.md)\n", []string{"a.md", "b.md"}, true, classBehavioral, 1, 0},
		{"dangling link", "- [A](a.md)\n- [Gone](gone.md) — was here\n- [Web](https://x.com/y.md)\n", []string{"a.md"}, true, classReducible, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := memoryHealthEnv(t)
			dir := autoMemoryDir(repo)
			mustWrite(t, filepath.Join(dir, "MEMORY.md"), tc.index)
			for _, f := range tc.files {
				mustWrite(t, filepath.Join(dir, f), "note\n")
			}
			got := memorySinksWithPrefix(memoryHealthSinks(repo, 10), "memory_health:memory_orphans:")
			if (len(got) == 1) != tc.wantHit {
				t.Fatalf("hit=%v want %v: %+v", len(got) == 1, tc.wantHit, got)
			}
			if !tc.wantHit {
				return
			}
			s := got[0]
			if s.Class != tc.wantClass || s.Evidence["orphan_count"] != tc.wantOrphans || s.Evidence["dangling_count"] != tc.wantDangling {
				t.Fatalf("sink = %+v", s)
			}
			if (s.TokensPerTurn > 0) != (tc.wantClass == classReducible) {
				t.Fatalf("tokens/turn only for reducible dead links: %+v", s)
			}
		})
	}
}

func TestBrokenImports(t *testing.T) {
	_, repo := memoryHealthEnv(t)
	mustWrite(t, filepath.Join(repo, "exists.md"), "x\n")
	mustWrite(t, filepath.Join(repo, "CLAUDE.md"), strings.Join([]string{
		"@./missing.md",
		"Also import @docs/gone.md, please.",
		"And @~/nope.md",
		"@./exists.md",
		"Literal `@./code-span.md` stays literal.",
		"Mail julius@example.com or ping @someuser about @anthropic-ai/sdk.",
		"Globs @src/*.ts and placeholders @<name>.md are not imports.",
		"See https://example.com/@user/file.md",
		"Ask @john.doe, pin @v1.2, see @config.local and @//fileserver/share/rules.md.",
		"```",
		"@./fenced.md",
		"```",
	}, "\n"))
	got := memorySinksWithPrefix(memoryHealthSinks(repo, 0), "memory_health:broken_imports:")
	if len(got) != 1 {
		t.Fatalf("want one broken_imports sink, got %+v", got)
	}
	imports := got[0].Evidence["imports"].([]string)
	want := []string{"1: @./missing.md", "2: @docs/gone.md", "3: @~/nope.md"}
	if strings.Join(imports, "|") != strings.Join(want, "|") {
		t.Fatalf("imports = %q want %q", imports, want)
	}
	if got[0].Class != classBehavioral {
		t.Fatalf("class = %s", got[0].Class)
	}
}

func TestDuplicateRules(t *testing.T) {
	claude, repo := memoryHealthEnv(t)
	rule := "Always run the full test suite before you commit anything."
	mustWrite(t, filepath.Join(claude, "CLAUDE.md"), strings.Join([]string{
		"# Rules", "- " + rule, "- Be terse.", "## Shared heading with enough words in it here",
		"```", "go test ./... -run everything in the repo now", "```",
	}, "\n"))
	mustWrite(t, filepath.Join(repo, "CLAUDE.md"), strings.Join([]string{
		"* **" + strings.ToLower(strings.TrimSuffix(rule, ".")) + "**", "- Be terse.", "## Shared heading with enough words in it here",
		"```", "go test ./... -run everything in the repo now", "```",
	}, "\n"))
	// Codex loads AGENTS.md, not CLAUDE.md: a shared line is not a Claude duplicate.
	mustWrite(t, filepath.Join(repo, "AGENTS.md"), "- Never push to main without a reviewed pull request first.\n")
	mustWrite(t, filepath.Join(claude, "..", "codex", "AGENTS.md"), "- Never push to main without a reviewed pull request first.\n")
	// MEMORY.md: a copy inside the loaded window counts, one past the cutoff does not.
	mustWrite(t, filepath.Join(autoMemoryDir(repo), "MEMORY.md"),
		"- "+rule+"\n"+numberedLines(210, "- entry %d")+"- Prefer small reviewed diffs over giant rewrites always.\n")
	mustWrite(t, filepath.Join(repo, "CLAUDE.local.md"), "- Prefer small reviewed diffs over giant rewrites always.\n")

	sinks := memoryHealthSinks(repo, 10)
	claudeDup := memorySinksWithPrefix(sinks, "memory_health:duplicate_rules:claude")
	if len(claudeDup) != 1 {
		t.Fatalf("want claude duplicate sink, got %+v", sinks)
	}
	s := claudeDup[0]
	if s.Evidence["duplicate_count"] != 1 {
		t.Fatalf("only the test-suite rule should duplicate: %+v", s.Evidence)
	}
	if !strings.HasPrefix(s.Title, "1 rule loads more than once in each Claude session") {
		t.Fatalf("singular title = %q", s.Title)
	}
	perCopy, _ := configTokenCount("- " + rule)
	if s.Class != classReducible || s.TokensPerTurn != int64(2*perCopy) || s.TokensPerDayRate != int64(20*perCopy) {
		t.Fatalf("sink = %+v (per copy %d, 3 copies)", s, perCopy)
	}
	codexDup := memorySinksWithPrefix(sinks, "memory_health:duplicate_rules:codex")
	if len(codexDup) != 1 {
		t.Fatalf("codex user+project AGENTS.md share a rule: %+v", sinks)
	}
}

func TestStaleReferences(t *testing.T) {
	_, repo := memoryHealthEnv(t)
	mustWrite(t, filepath.Join(repo, "src", "ok.ts"), "x")
	mustWrite(t, filepath.Join(repo, "docs", "a.md"), "x")
	mustWrite(t, filepath.Join(repo, ".github", "workflows", "ci.yml"), "x")
	mustWrite(t, filepath.Join(repo, "CLAUDE.md"), strings.Join([]string{
		"Edit `src/gone.ts` and `src/ok.ts` (see `src/gone.ts:12`).",
		"Also `src/moved/` and `docs/a.md`.",
		"Globs `src/*.ts`, placeholders `src/<name>.ts`, `docs/NN-id.md`, urls `https://x.com/src/a.ts`.",
		"Written into other repos: `.github/copilot-instructions.md`, `.cursor/rules/x.mdc`.",
		"Not anchored here: `nothere/x.ts`. Bare file `README.md`. Command `npm run build`.",
		"We removed `src/old.ts` last month.",
		"```",
		"`src/fenced.ts`",
		"```",
	}, "\n"))
	got := memorySinksWithPrefix(memoryHealthSinks(repo, 0), "memory_health:stale_references:")
	if len(got) != 1 {
		t.Fatalf("want one stale sink, got %+v", got)
	}
	refs := got[0].Evidence["references"].([]string)
	want := []string{"1: src/gone.ts", "2: src/moved/"}
	if strings.Join(refs, "|") != strings.Join(want, "|") {
		t.Fatalf("refs = %q want %q", refs, want)
	}
}

func TestBuriedRules(t *testing.T) {
	build := func(n int, at map[int]string) string {
		lines := make([]string, n)
		for i := range lines {
			lines[i] = fmt.Sprintf("plain line %d", i+1)
		}
		for i, l := range at {
			lines[i-1] = l
		}
		return strings.Join(lines, "\n")
	}
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"middle band", build(200, map[int]string{100: "NEVER do x", 110: "You MUST do y", 120: "IMPORTANT: z", 130: "never lowercase", 5: "ALWAYS top"}), 3},
		{"top and bottom only", build(200, map[int]string{5: "NEVER a", 10: "MUST b", 195: "ALWAYS c"}), 0},
		{"short file", build(100, map[int]string{40: "NEVER a", 50: "MUST b", 60: "ALWAYS c"}), 0},
		{"fenced", build(200, map[int]string{90: "```", 100: "NEVER a", 101: "MUST b", 102: "ALWAYS c", 110: "```"}), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := memoryHealthEnv(t)
			mustWrite(t, filepath.Join(repo, "CLAUDE.md"), tc.body)
			got := memorySinksWithPrefix(memoryHealthSinks(repo, 0), "memory_health:buried_rules:")
			if tc.want == 0 {
				if len(got) != 0 {
					t.Fatalf("false positive: %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Evidence["count"] != tc.want || !strings.Contains(got[0].Suggestion, "Rule of thumb") {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestAutoMemoryDirResolution(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CAVEMAN_CLAUDE_ROOT", "")
	t.Setenv("CLAUDE_CODE_PROJECT_DIR_NAME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "cfg"))
	main := filepath.Join(base, "main.repo")
	mustMkdir(t, filepath.Join(main, ".git", "worktrees", "wt"))
	wt := filepath.Join(base, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(main, ".git", "worktrees", "wt")+"\n")
	mustMkdir(t, filepath.Join(wt, "sub"))

	slug := nonAlnum.ReplaceAllString(main, "-")
	want := filepath.Join(base, "cfg", "projects", slug, "memory")
	for _, cwd := range []string{main, wt, filepath.Join(wt, "sub")} {
		if got := autoMemoryDir(cwd); got != want {
			t.Fatalf("autoMemoryDir(%s) = %s want %s", cwd, got, want)
		}
	}
	t.Setenv("CLAUDE_CODE_PROJECT_DIR_NAME", "shared")
	if got := autoMemoryDir(wt); got != filepath.Join(base, "cfg", "projects", "shared", "memory") {
		t.Fatalf("project dir name override ignored: %s", got)
	}
}

func TestMemoryHealthInLearnPlan(t *testing.T) {
	_, repo := memoryHealthEnv(t)
	mustWrite(t, filepath.Join(repo, "CLAUDE.md"), "@./missing.md\n")
	plan, err := openRetroTestStore(t).BuildLearnPlan(repo, []string{"claude"}, "7d")
	if err != nil {
		t.Fatal(err)
	}
	if got := memorySinksWithPrefix(plan.Sinks, "memory_health:broken_imports:"); len(got) != 1 || got[0].Basis != learnBasis {
		t.Fatalf("memory_health sink missing from plan: %+v", plan.Sinks)
	}
}

func TestLearnReportListsEveryMemoryHealthFinding(t *testing.T) {
	var sinks []Sink
	for i := range 25 {
		sinks = append(sinks, Sink{SinkID: fmt.Sprintf("config_tax:x%d", i), Title: fmt.Sprintf("busy sink %d", i), Class: classReducible, Basis: learnBasis, TokensPerTurn: 100, TokensPerDayRate: int64(10_000 - i)})
	}
	sinks = append(sinks, memorySink("broken_imports", "abcd1234", "CLAUDE.md (project) has 1 @import that points to a missing file", classBehavioral, 0, 0,
		map[string]any{"path": "/repo/CLAUDE.md"}, "Fix or remove the @import."))
	render := func(sinks []Sink) string {
		t.Helper()
		out := filepath.Join(t.TempDir(), "learn.html")
		if err := (&Store{}).WriteLearnHTML(LearnPlan{Schema: learnSchema, Basis: learnBasis, Sinks: sinks}, out); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(out)
		return string(raw)
	}
	html := render(sinks)
	at := strings.Index(html, "<h2>Memory and instruction files</h2>")
	if at < 0 {
		t.Fatal("memory & rules section missing")
	}
	for _, want := range []string{"Broken @imports", "has 1 @import that points", "Fix or remove the @import.", "/repo/CLAUDE.md", "This finding is listed here."} {
		if !strings.Contains(html[at:], want) {
			t.Errorf("memory section missing %q", want)
		}
	}
	if strings.Contains(render(sinks[:25]), "Memory and instruction files") {
		t.Error("section must be omitted without memory findings")
	}
}

func TestMemoryHealthTitlesUseSingular(t *testing.T) {
	_, repo := memoryHealthEnv(t)
	mustWrite(t, filepath.Join(repo, "CLAUDE.md"), "@./missing.md\nEdit `src/gone.ts` now.\n")
	mustMkdir(t, filepath.Join(repo, "src"))
	sinks := memoryHealthSinks(repo, 0)
	for prefix, want := range map[string]string{
		"memory_health:broken_imports:":   "CLAUDE.md (project) has 1 @import that points to a missing file",
		"memory_health:stale_references:": "CLAUDE.md names 1 repo path that no longer exists",
	} {
		got := memorySinksWithPrefix(sinks, prefix)
		if len(got) != 1 || got[0].Title != want {
			t.Errorf("%s title = %+v, want %q", prefix, got, want)
		}
	}
}

func TestBrokenImportsSkipsCodexOnlyFiles(t *testing.T) {
	_, repo := memoryHealthEnv(t)
	// A project CLAUDE.md exists, so Claude does not read AGENTS.md: it is Codex-only.
	mustWrite(t, filepath.Join(repo, "CLAUDE.md"), "- be terse\n")
	mustWrite(t, filepath.Join(repo, "AGENTS.md"), "@./missing.md\n")
	for _, s := range memorySinksWithPrefix(memoryHealthSinks(repo, 0), "memory_health:broken_imports:") {
		if s.Evidence["path"] == filepath.Join(repo, "AGENTS.md") {
			t.Fatalf("Codex-only AGENTS.md checked for @imports: %+v", s)
		}
	}
	mustWrite(t, filepath.Join(repo, "GEMINI.md"), "@./gone.md\n")
	if got := memorySinksWithPrefix(memoryHealthSinks(repo, 0), "memory_health:broken_imports:"); len(got) != 1 || got[0].Evidence["path"] != filepath.Join(repo, "GEMINI.md") {
		t.Fatalf("GEMINI.md @imports must still be checked: %+v", got)
	}
}

func TestMemorySinkIDsAndRepoAreRepoSpecific(t *testing.T) {
	_, repo := memoryHealthEnv(t)
	other := filepath.Join(filepath.Dir(repo), "other")
	mustMkdir(t, filepath.Join(other, ".git"))
	var ids []string
	for _, r := range []string{repo, other} {
		mustWrite(t, filepath.Join(autoMemoryDir(r), "MEMORY.md"), numberedLines(260, "- entry %d"))
		got := memorySinksWithPrefix(memoryHealthSinks(r, 0), "memory_health:memory_truncation:")
		if len(got) != 1 || got[0].Evidence["repo"] != filepath.Base(r) {
			t.Fatalf("%s truncation = %+v", r, got)
		}
		ids = append(ids, got[0].SinkID)
	}
	if ids[0] == ids[1] {
		t.Fatalf("truncation in two repos shares one id %q", ids[0])
	}
}
