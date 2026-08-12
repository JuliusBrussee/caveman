package repointel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCreatesDeterministicMapAndTaskEvidenceWithoutSensitiveContent(t *testing.T) {
	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/repo\n\ngo 1.26\n")
	write("auth/refresh.go", "package auth\n\nimport \"errors\"\n\nfunc RotateToken() error { return errors.New(\"x\") }\n")
	write("auth/refresh_test.go", "package auth\n\nfunc TestRotateToken() {}\n")
	write("AGENTS.md", "Never invent auth behavior.\n")
	write(".env", "SECRET_TOKEN=must-not-enter-map\n")
	runGit := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=RepoIntel", "GIT_AUTHOR_EMAIL=repo@example.test", "GIT_COMMITTER_NAME=RepoIntel", "GIT_COMMITTER_EMAIL=repo@example.test")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	runGit("init", "-q")
	runGit("add", ".")
	runGit("commit", "-qm", "auth refresh")

	repoMap, bundle, err := Build(context.Background(), root, "git:abc", []string{"auth", "rotate", "token"})
	if err != nil {
		t.Fatal(err)
	}
	if repoMap.Schema != MapSchema || repoMap.ContentSHA256 == "" || repoMap.ParserBasis == "" || repoMap.Truncated {
		t.Fatalf("wrong map metadata: %+v", repoMap)
	}
	var source, testFile *File
	for i := range repoMap.Files {
		switch repoMap.Files[i].Path {
		case "auth/refresh.go":
			source = &repoMap.Files[i]
		case "auth/refresh_test.go":
			testFile = &repoMap.Files[i]
		}
	}
	if source == nil || len(source.Symbols) == 0 || source.Symbols[0].Name != "RotateToken" || source.Package != "." || source.RecentChanges == 0 {
		t.Fatalf("source map incomplete: %+v", source)
	}
	if testFile == nil || testFile.TestFor != "auth/refresh.go" {
		t.Fatalf("test relationship missing: %+v", testFile)
	}
	rawMap := strings.Builder{}
	for _, file := range repoMap.Files {
		rawMap.WriteString(file.Path)
		for _, symbol := range file.Symbols {
			rawMap.WriteString(symbol.Name)
		}
	}
	if strings.Contains(rawMap.String(), "must-not-enter-map") {
		t.Fatal("sensitive file content entered repository map")
	}
	if bundle.Schema != BundleSchema || bundle.EvidenceStatus != "observed_local_repository_metadata" || len(bundle.Items) == 0 || bundle.Items[0].Path == "" {
		t.Fatalf("evidence bundle missing: %+v", bundle)
	}
	again, againBundle, err := Build(context.Background(), root, "git:abc", []string{"auth", "rotate", "token"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ContentSHA256 != repoMap.ContentSHA256 || len(againBundle.Items) != len(bundle.Items) {
		t.Fatalf("repository intelligence is not deterministic: %s != %s", again.ContentSHA256, repoMap.ContentSHA256)
	}
}

func TestNormalizeTermsIsBoundedAndFailClosed(t *testing.T) {
	terms := NormalizeTerms([]string{"Auth", "../../secret", "x", "auth", strings.Repeat("a", 65), "token"})
	if strings.Join(terms, ",") != "auth,token" {
		t.Fatalf("normalized terms = %v", terms)
	}
}

func TestImpactTestsUsesDirectAndPackageRelationshipsWithoutCoverageClaims(t *testing.T) {
	repoMap := Map{Files: []File{
		{Path: "auth/refresh.go", Package: "auth"},
		{Path: "auth/refresh_test.go", Package: "auth", TestFor: "auth/refresh.go"},
		{Path: "auth/session_test.go", Package: "auth", TestFor: "auth/session.go"},
		{Path: "billing/cost_test.go", Package: "billing", TestFor: "billing/cost.go"},
	}}
	impact := ImpactTests(repoMap, []string{"auth/refresh.go", "../../escape"})
	if strings.Join(impact.ChangedPaths, ",") != "auth/refresh.go" || strings.Join(impact.AffectedTests, ",") != "auth/refresh_test.go,auth/session_test.go" {
		t.Fatalf("wrong conservative impact: %+v", impact)
	}
	if impact.Basis != "direct_test_to_source_plus_same_package_conservative" || !impact.Conservative || impact.EvidenceStatus != "observed_local_repository_metadata" {
		t.Fatalf("impact overclaims basis: %+v", impact)
	}
}
