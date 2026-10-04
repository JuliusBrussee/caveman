package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// learnWireEnv isolates every root learn reads: no test touches the real
// ~/.claude, ~/.codex, or ~/.caveman.
func learnWireEnv(t *testing.T) (home, claudeRoot string) {
	t.Helper()
	home, claudeRoot = t.TempDir(), t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_DB", filepath.Join(home, "caveman.db"))
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeRoot)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_GEMINI_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_OPENCODE_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_AIDER_ROOT", "")
	t.Chdir(t.TempDir())
	return home, claudeRoot
}

func writeLearnSession(t *testing.T, claudeRoot, repo, name string, lines ...string) {
	t.Helper()
	dir := filepath.Join(claudeRoot, "projects", repo)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func usageLine(id string, at time.Time, tokens int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"model":"claude-sonnet-4-6","usage":{"input_tokens":%d}}}`,
		at.UTC().Format(time.RFC3339), id, tokens)
}

func learnJSON(t *testing.T, out any, args ...string) {
	t.Helper()
	raw := captureStdout(t, func() { runLearn(slog.New(slog.NewTextHandler(io.Discard, nil)), args) })
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		t.Fatalf("learn %v output %q: %v", args, raw, err)
	}
}

// TestLearnWireHelperProcess runs runLearn in a child so failure paths, which
// exit the process, can be asserted on exit code and stderr.
func TestLearnWireHelperProcess(t *testing.T) {
	raw := os.Getenv("CAVEMAN_TEST_LEARN_ARGS")
	if raw == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		os.Exit(3)
	}
	runLearn(slog.New(slog.NewJSONHandler(os.Stderr, nil)), args)
	os.Exit(0)
}

func learnFails(t *testing.T, want string, args ...string) {
	t.Helper()
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(os.Args[0], "-test.run=^TestLearnWireHelperProcess$")
	cmd.Env = append(os.Environ(), "CAVEMAN_TEST_LEARN_ARGS="+string(encoded))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() == 0 {
		t.Fatalf("learn %v: err=%v, want non-zero exit; stdout=%q", args, err, stdout.String())
	}
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("learn %v stderr = %q, want %q", args, stderr.String(), want)
	}
}

func TestLearnExperimentLifecycleEndToEnd(t *testing.T) {
	home, claudeRoot := learnWireEnv(t)

	var started store.Experiment
	learnJSON(t, &started, "experiment", "start", "distill", "--sink", "procedure_repeat:abc", "--fix-kind", "skill_distillation")
	if started.Label != "distill" || started.SinkID != "procedure_repeat:abc" || started.FixKind != "skill_distillation" ||
		len(started.Arms) != 1 || started.Arms[0].Arm != "on" {
		t.Fatalf("start = %+v", started)
	}
	var switched store.Experiment
	learnJSON(t, &switched, "experiment", "arm", "distill", "off")
	if len(switched.Arms) != 2 || switched.Arms[0].EndedAt == "" || switched.Arms[1].Arm != "off" {
		t.Fatalf("arm off = %+v", switched.Arms)
	}

	// Backdate the arms so real session timestamps can fall inside them.
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	db, err := sql.Open("sqlite", filepath.Join(home, "caveman.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`UPDATE experiments SET created_at = ?`, []any{at(30 * 24 * time.Hour)}},
		{`UPDATE experiment_arms SET started_at = ?, ended_at = ? WHERE arm = 'on'`, []any{at(30 * 24 * time.Hour), at(15 * 24 * time.Hour)}},
		{`UPDATE experiment_arms SET started_at = ? WHERE arm = 'off'`, []any{at(15 * 24 * time.Hour)}},
	} {
		if _, err := db.Exec(stmt.q, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	var report store.ExperimentReport
	learnJSON(t, &report, "experiment", "report", "distill")
	if report.Verdict != "insufficient_data" || report.DeltaPct != nil {
		t.Fatalf("empty report = %+v", report)
	}

	for i := 0; i < 5; i++ {
		on := now.Add(-25*24*time.Hour + time.Duration(i)*time.Hour)
		off := now.Add(-10*24*time.Hour + time.Duration(i)*time.Hour)
		writeLearnSession(t, claudeRoot, "repo", fmt.Sprintf("on-%d", i), usageLine(fmt.Sprintf("on%d", i), on, 1000))
		writeLearnSession(t, claudeRoot, "repo", fmt.Sprintf("off-%d", i), usageLine(fmt.Sprintf("off%d", i), off, 2000))
	}
	// The default window is the experiment's lifetime (30d here), so both arms
	// count even though nothing was passed.
	learnJSON(t, &report, "experiment", "report", "distill")
	if report.Verdict != "improved" || report.DeltaPct == nil || *report.DeltaPct != -50 || len(report.Arms) != 2 ||
		report.Arms[0].Sessions != 5 || report.Arms[1].Sessions != 5 {
		t.Fatalf("report = %+v", report)
	}
	// An explicit --since still wins: 7d drops the off-arm, so no verdict.
	learnJSON(t, &report, "experiment", "report", "distill", "--since", "7d")
	if report.Verdict != "insufficient_data" {
		t.Fatalf("7d report = %+v", report)
	}

	var list []store.Experiment
	learnJSON(t, &list, "experiment", "list")
	if len(list) != 1 || list[0].Label != "distill" {
		t.Fatalf("list = %+v", list)
	}
	var stopped store.Experiment
	learnJSON(t, &stopped, "experiment", "stop", "distill")
	if stopped.StoppedAt == "" {
		t.Fatalf("stop = %+v", stopped)
	}
	learnFails(t, "is stopped", "experiment", "arm", "distill", "on")
	learnFails(t, `no experiment named \"nope\"`, "experiment", "report", "nope")
	learnFails(t, "arm <label> on|off", "experiment", "arm", "distill")
	learnFails(t, "usage: caveman-proxy learn experiment", "experiment", "bogus")
}

func TestLearnExportWritesPrivacySafeDigest(t *testing.T) {
	home, claudeRoot := learnWireEnv(t)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		writeLearnSession(t, claudeRoot, "secret-client-repo", fmt.Sprintf("s%d", i), usageLine(fmt.Sprintf("m%d", i), now.Add(-time.Duration(i+1)*time.Hour), 1000))
	}

	var out struct {
		Path    string            `json:"path"`
		Summary string            `json:"summary"`
		Digest  store.LearnDigest `json:"digest"`
	}
	learnJSON(t, &out, "export")
	if want := filepath.Join(home, "reports", "caveman-learn-digest.json"); out.Path != want {
		t.Fatalf("default path = %q, want %q", out.Path, want)
	}
	if out.Digest.Schema != "caveman.learn.digest.v1" || out.Digest.SessionsScanned != 3 || !strings.Contains(out.Summary, "nothing sent") {
		t.Fatalf("export = %+v", out)
	}
	info, err := os.Stat(out.Path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not preserve POSIX owner-only permission bits. The file is
	// still created with the requested mode on platforms that implement it.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("digest mode = %v, want 0600", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(out.Path)
	if strings.Contains(string(raw), "secret-client-repo") || !strings.Contains(string(raw), "caveman.learn.digest.v1") {
		t.Fatalf("digest file leaks or is empty: %s", raw)
	}

	custom := filepath.Join(t.TempDir(), "nested", "d.json")
	learnJSON(t, &out, "export", "--out", custom)
	if out.Path != custom {
		t.Fatalf("--out path = %q", out.Path)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatal(err)
	}
}

func TestLearnReconcileComparesBilledAgainstMeasured(t *testing.T) {
	_, claudeRoot := learnWireEnv(t)
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		writeLearnSession(t, claudeRoot, "repo", fmt.Sprintf("s%d", i), usageLine(fmt.Sprintf("m%d", i), now.Add(-time.Duration(i+1)*time.Hour), 1000))
	}
	csv := filepath.Join(t.TempDir(), "usage.csv")
	if err := os.WriteFile(csv, []byte("model,input_tokens,output_tokens\nclaude-sonnet-4-6,8000,0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var report store.LearnReconcile
	learnJSON(t, &report, "reconcile", "--usage-export", csv)
	if report.BilledTokens != 8000 || report.MeasuredTokens != 2000 || report.CoveragePct != 25 || report.Unattributed != 6000 ||
		len(report.Rows) != 1 || report.Rows[0].Model != "claude-sonnet-4-6" {
		t.Fatalf("reconcile = %+v", report)
	}
	learnFails(t, "--usage-export <csv>", "reconcile")
	bad := filepath.Join(t.TempDir(), "bad.csv")
	_ = os.WriteFile(bad, []byte("foo,bar\n1,2\n"), 0o600)
	learnFails(t, "command failed", "reconcile", "--usage-export", bad)
}

// TestLearnApplyHonorsRepoFilter: apply must rebuild the same plan the user
// saw. A sink that only exists in repo beta cannot be applied under --repo
// alpha, and one found under a filter must still be found with it.
func TestLearnApplyHonorsRepoFilter(t *testing.T) {
	_, claudeRoot := learnWireEnv(t)
	now := time.Now().UTC()
	block := strings.TrimSpace(strings.Repeat("project policy context stays identical across sessions ", 30))
	text, _ := json.Marshal(map[string]any{"type": "user", "timestamp": now.Add(-time.Hour).Format(time.RFC3339),
		"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": block}}}})
	for i := 0; i < 3; i++ {
		writeLearnSession(t, claudeRoot, "beta", fmt.Sprintf("b%d", i),
			usageLine(fmt.Sprintf("b%da", i), now.Add(-2*time.Hour), 20000), string(text),
			usageLine(fmt.Sprintf("b%db", i), now.Add(-time.Hour), 20000))
	}
	writeLearnSession(t, claudeRoot, "alpha", "a0", usageLine("a0", now.Add(-time.Hour), 1000))

	var sim struct {
		Rows []struct {
			SinkID string `json:"sink_id"`
		} `json:"per_sink"`
	}
	spend, err := store.Open(filepath.Join(os.Getenv("CAVEMAN_HOME"), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	plan, err := spend.BuildLearnPlanFilteredWithRetro(cwd, nil, "30d", store.RetroOptions{}, "beta")
	_ = spend.Close()
	if err != nil {
		t.Fatal(err)
	}
	sinkID := ""
	for _, sink := range plan.Sinks {
		if strings.HasPrefix(sink.SinkID, "recurring_context:repaste:") {
			sinkID = sink.SinkID
		}
	}
	if sinkID == "" {
		t.Fatalf("fixture produced no recurring sink: %+v", plan.Sinks)
	}

	var applied map[string]any
	learnJSON(t, &applied, "apply", sinkID, "--dry-run", "--repo", "beta")
	if applied["sink_id"] != sinkID || applied["dry_run"] != true {
		t.Fatalf("apply under --repo beta = %+v", applied)
	}
	learnFails(t, "not found in current learn plan", "apply", sinkID, "--dry-run", "--repo", "alpha")
	learnJSON(t, &sim, "simulate", sinkID, "--repo", "beta")
	if len(sim.Rows) != 1 || sim.Rows[0].SinkID != sinkID {
		t.Fatalf("simulate under --repo beta = %+v", sim)
	}
}

func TestLearnScanNoRememberSkipsDurableLearnings(t *testing.T) {
	home, claudeRoot := learnWireEnv(t)
	var lines []string
	for i := range 400 {
		lines = append(lines, fmt.Sprintf("- rule %d: keep this project tidy and well documented at all times", i))
	}
	if err := os.WriteFile(filepath.Join(claudeRoot, "CLAUDE.md"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	learnings := func() int {
		t.Helper()
		db, err := sql.Open("sqlite", filepath.Join(home, "caveman.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM learnings WHERE source_kind = 'caveman_learn'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var plan store.LearnPlan
	learnJSON(t, &plan, "scan", "--no-remember")
	if len(plan.Sinks) == 0 || learnings() != 0 {
		t.Fatalf("--no-remember wrote learnings: sinks=%d learnings=%d", len(plan.Sinks), learnings())
	}
	learnJSON(t, &plan, "scan")
	if learnings() == 0 {
		t.Fatal("a plain scan should still record learnings")
	}
}

func TestLearnScanReportsHomeLeavesCanonicalReportAlone(t *testing.T) {
	home, _ := learnWireEnv(t)
	alt := filepath.Join(home, "runtime", "learn-autopilot")
	var plan store.LearnPlan
	learnJSON(t, &plan, "scan", "--write-report", "--no-remember", "--reports-home", alt)
	if _, err := os.Stat(filepath.Join(alt, "reports", "caveman-learn.json")); err != nil {
		t.Fatalf("autopilot report missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(alt, "reports", "caveman-learn.html")); err != nil {
		t.Fatalf("autopilot html missing: %v", err)
	}
	if entries, _ := filepath.Glob(filepath.Join(home, "reports", "caveman-learn*")); len(entries) != 0 {
		t.Fatalf("--reports-home touched canonical reports: %v", entries)
	}
}

func TestLearnExperimentStartSkipsFlagValuesForLabel(t *testing.T) {
	learnWireEnv(t)
	var out struct {
		Label   string `json:"label"`
		FixKind string `json:"fix_kind"`
		Note    string `json:"note"`
	}
	learnJSON(t, &out, "experiment", "start", "--note", "trim", "--fix-kind", "claude_md_weight", "mylabel")
	if out.Label != "mylabel" || out.FixKind != "claude_md_weight" {
		t.Fatalf("experiment start = %+v", out)
	}
}

func TestLearnCapabilitiesAnswersWithoutAStore(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	t.Setenv("CAVEMAN_HOME", home)
	var caps map[string]any
	learnJSON(t, &caps, "capabilities")
	if caps["schema"] != "caveman.learn.capabilities.v1" || caps["no_remember"] != true || caps["reports_home"] != true || caps["memory_health"] != true {
		t.Fatalf("capabilities = %v", caps)
	}
	if _, err := os.Stat(home); err == nil {
		t.Fatal("capabilities must not create a store")
	}
}
