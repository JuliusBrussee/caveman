package repointel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func runGitIn(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=RepoIntel", "GIT_AUTHOR_EMAIL=repo@example.test", "GIT_COMMITTER_NAME=RepoIntel", "GIT_COMMITTER_EMAIL=repo@example.test")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

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
	runGitIn(t, root, "init", "-q")
	runGitIn(t, root, "add", ".")
	runGitIn(t, root, "commit", "-qm", "auth refresh")

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

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, body := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mappedPaths(repoMap Map) []string {
	out := make([]string, 0, len(repoMap.Files))
	for _, file := range repoMap.Files {
		out = append(out, file.Path)
	}
	return out
}

// forceWalkListing removes the git listing so a test provably exercises the
// filesystem walk, instead of depending on TMPDIR not sitting in a checkout.
func forceWalkListing(t *testing.T) {
	t.Helper()
	original := gitListFiles
	gitListFiles = func(context.Context, string) ([]string, bool, bool) { return nil, false, false }
	t.Cleanup(func() { gitListFiles = original })
}

// Dependency trees are identified by their contents, not by a name list: a
// pixi/conda prefix and a virtualenv are excluded under any directory name,
// including the interpreter standard library they carry.
func TestWalkExcludesInstalledEnvironmentsByMarkerNotName(t *testing.T) {
	forceWalkListing(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/proxy.py":                         "def proxy():\n    return 1\n",
		"src/env/settings.py":                  "def proxy_settings():\n    return 2\n",
		".pixi/envs/default/conda-meta/x.json": "{}",
		".pixi/envs/default/lib/python3.14/site-packages/zmq/proxydevice.py": "class ProxyDevice:\n    pass\n",
		".pixi/envs/default/lib/python3.14/xml/sax/xmlreader.py":             "class XMLReader:\n    pass\n",
		// A conda prefix under an ordinary name: only the marker gives it away.
		"envs/py314/conda-meta/history":                 "",
		"envs/py314/lib/python3.14/email/mime/image.py": "def proxy():\n    pass\n",
		// A virtualenv that is not called venv.
		"toolchain/pyvenv.cfg":                                 "home = /usr\n",
		"toolchain/lib/python3.14/site-packages/proxy_util.py": "def proxy():\n    pass\n",
		"node_modules/leftpad/index.js":                        "export function proxy() {}\n",
	})

	repoMap, bundle, err := Build(context.Background(), root, "git:markers", []string{"proxy"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(mappedPaths(repoMap), ",")
	if got != "src/env/settings.py,src/proxy.py" {
		t.Fatalf("map should hold project source only, got: %s", got)
	}
	if len(bundle.Items) == 0 || bundle.Items[0].Path != "src/proxy.py" || !bundle.Items[0].Direct {
		t.Fatalf("evidence should rank project source directly: %+v", bundle.Items)
	}
	if bundle.Strength != StrengthDirect || !bundle.HasDirectEvidence() {
		t.Fatalf("path-term match must count as direct evidence: %+v", bundle)
	}
}

// git already knows which files are source: an ignored dependency tree never
// reaches the index even when its directory name is unknown to us.
func TestBuildPrefersGitIgnoreRulesOverNameHeuristics(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":                    "/deps-cache/\n",
		"src/proxy.go":                  "package src\n\nfunc Proxy() {}\n",
		"deps-cache/zmq/proxydevice.go": "package zmq\n\nfunc Proxy() {}\n",
		"deps-cache/xml/xmlreader.go":   "package xml\n\nfunc Proxy() {}\n",
	})
	runGitIn(t, root, "init", "-q")
	runGitIn(t, root, "add", ".")
	runGitIn(t, root, "commit", "-qm", "source")

	repoMap, _, err := Build(context.Background(), root, "git:ignored", []string{"proxy"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range mappedPaths(repoMap) {
		if strings.HasPrefix(path, "deps-cache/") {
			t.Fatalf("gitignored dependency tree entered the map: %v", mappedPaths(repoMap))
		}
	}
	if !slices.Contains(mappedPaths(repoMap), "src/proxy.go") {
		t.Fatalf("tracked source missing: %v", mappedPaths(repoMap))
	}
	if repoMap.ListingBasis != ListingGit {
		t.Fatalf("listing basis must disclose the git path: %q", repoMap.ListingBasis)
	}
}

// Everything git lists still goes through the content-based filter: a
// .gitignore that forgot an installed environment, or a committed dependency
// tree, must not reach the model just because git reported the file.
func TestGitListedPathsStillGetDependencyFiltering(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":       "module example.test/repo\n\ngo 1.26\n",
		"src/proxy.go": "package src\n\nfunc Proxy() {}\n",
		// Untracked, un-ignored conda prefix under a name no list contains.
		"toolbox/py314/conda-meta/history":                    "",
		"toolbox/py314/lib/python3.14/xml/sax/xmlreader.py":   "class XMLReader:\n    pass\n",
		"toolbox/py314/lib/python3.14/site-packages/proxy.py": "def proxy():\n    pass\n",
		// Committed Go vendor tree: tracked, so only the manifest rule catches it.
		"vendor/modules.txt":                     "# example\n",
		"vendor/github.com/x/zmq/proxydevice.go": "package zmq\n\nfunc Proxy() {}\n",
	})
	runGitIn(t, root, "init", "-q")
	runGitIn(t, root, "add", ".")
	runGitIn(t, root, "commit", "-qm", "source and vendor")

	repoMap, bundle, err := Build(context.Background(), root, "git:filtered", []string{"proxy"})
	if err != nil {
		t.Fatal(err)
	}
	if repoMap.ListingBasis != ListingGit {
		t.Fatalf("expected the git listing path: %q", repoMap.ListingBasis)
	}
	for _, path := range mappedPaths(repoMap) {
		if strings.HasPrefix(path, "toolbox/") || strings.HasPrefix(path, "vendor/") {
			t.Fatalf("dependency tree survived the git listing: %v", mappedPaths(repoMap))
		}
	}
	if !slices.Contains(mappedPaths(repoMap), "src/proxy.go") {
		t.Fatalf("project source missing: %v", mappedPaths(repoMap))
	}
	if len(bundle.Items) == 0 || bundle.Items[0].Path != "src/proxy.go" {
		t.Fatalf("evidence must rank project source: %+v", bundle.Items)
	}
}

// A directory that an enclosing repository ignores makes `git ls-files` exit 0
// with no output. Accepting that as a complete map silently disabled the whole
// mechanism, so an empty listing falls back to the walk.
func TestEmptyGitListingFallsBackToWalk(t *testing.T) {
	outer := t.TempDir()
	writeTree(t, outer, map[string]string{
		".gitignore":            "/scratch/\n",
		"outer.go":              "package outer\n",
		"scratch/proj/proxy.go": "package proj\n\nfunc Proxy() {}\n",
	})
	runGitIn(t, outer, "init", "-q")
	runGitIn(t, outer, "add", ".")
	runGitIn(t, outer, "commit", "-qm", "outer")

	root := filepath.Join(outer, "scratch", "proj")
	repoMap, bundle, err := Build(context.Background(), root, "git:nested", []string{"proxy"})
	if err != nil {
		t.Fatal(err)
	}
	if repoMap.ListingBasis != ListingWalk {
		t.Fatalf("empty git listing must fall back to the walk: %q", repoMap.ListingBasis)
	}
	if !slices.Contains(mappedPaths(repoMap), "proxy.go") {
		t.Fatalf("source in an ignored subdirectory must still be indexed: %v", mappedPaths(repoMap))
	}
	if !bundle.HasDirectEvidence() {
		t.Fatalf("direct hit lost to the empty-listing path: %+v", bundle)
	}
}

// Running git inside a repository executes what that repository's config says
// to execute. core.fsmonitor runs during an index read, so `ls-files` alone
// would be enough for a freshly cloned untrusted repository to run code.
func TestGitListingIgnoresRepositoryControlledCommands(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	root := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "executed")
	hook := filepath.Join(root, "fsmonitor-hook.sh")
	writeTree(t, root, map[string]string{"src/proxy.go": "package src\n\nfunc Proxy() {}\n"})
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+sentinel+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, root, "init", "-q")
	runGitIn(t, root, "add", ".")
	runGitIn(t, root, "commit", "-qm", "source")
	runGitIn(t, root, "config", "core.fsmonitor", hook)
	runGitIn(t, root, "config", "core.hooksPath", root)

	if _, _, err := Build(context.Background(), root, "git:hostile", []string{"proxy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("repository-controlled command executed during repository intelligence")
	}
}

// Ambiguous names are first-party until an ecosystem manifest says otherwise,
// so `internal/build` and a hand-written `vendor/` are not silently dropped.
func TestAmbiguousDirectoriesNeedEcosystemEvidence(t *testing.T) {
	forceWalkListing(t)
	kept := t.TempDir()
	writeTree(t, kept, map[string]string{
		"build/proxy_rules.py": "def proxy():\n    return 1\n",
		"vendor/proxy_shim.py": "def proxy():\n    return 2\n",
		"target/proxy_spec.py": "def proxy():\n    return 3\n",
	})
	repoMap, _, err := Build(context.Background(), kept, "git:ambiguous", []string{"proxy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repoMap.Files) != 3 {
		t.Fatalf("first-party dirs named build/vendor/target must survive: %v", mappedPaths(repoMap))
	}

	dropped := t.TempDir()
	writeTree(t, dropped, map[string]string{
		"go.mod":                "module example.test/repo\n\ngo 1.26\n",
		"Cargo.toml":            "[package]\nname = \"repo\"\n",
		"src/proxy.go":          "package src\n\nfunc Proxy() {}\n",
		"vendor/dep/proxy.go":   "package dep\n\nfunc Proxy() {}\n",
		"target/debug/proxy.go": "package debug\n\nfunc Proxy() {}\n",
	})
	repoMap, _, err = Build(context.Background(), dropped, "git:ambiguous", []string{"proxy"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range mappedPaths(repoMap) {
		if strings.HasPrefix(path, "vendor/") || strings.HasPrefix(path, "target/") {
			t.Fatalf("manifest-proven build output must be excluded: %v", mappedPaths(repoMap))
		}
	}
	if !slices.Contains(mappedPaths(repoMap), "src/proxy.go") {
		t.Fatalf("exclusion must not empty the map: %v", mappedPaths(repoMap))
	}
}

func TestExcludedPathFoldsCaseForUnambiguousNames(t *testing.T) {
	for _, path := range []string{
		".Pixi/envs/default/lib/x.py",
		"lib/Site-Packages/zmq/proxydevice.py",
		"Node_Modules/leftpad/index.js",
		"VENV/lib/python3.14/foo.py",
	} {
		if !excludedPath(path) {
			t.Fatalf("case variant not excluded: %s", path)
		}
	}
	for _, path := range []string{"src/env/settings.py", "internal/build/rules.go", "src/vendor_client.go"} {
		if excludedPath(path) {
			t.Fatalf("first-party path excluded: %s", path)
		}
	}
}

func TestEvidenceSkipsVendoredPathsAlreadyPresentInMap(t *testing.T) {
	bundle := Evidence(Map{Files: []File{
		{Path: ".pixi/envs/default/lib/python3.14/xml/sax/xmlreader.py", Symbols: []Symbol{{Name: "proxy", Kind: "function", LineStart: 107, LineEnd: 107}}},
		{Path: "site-packages/zmq/devices/proxydevice.py", Symbols: []Symbol{{Name: "ProxyDevice", Kind: "class", LineStart: 1, LineEnd: 4}}},
		{Path: "src/proxy.go", Symbols: []Symbol{{Name: "Proxy", Kind: "function", LineStart: 3, LineEnd: 10}}},
	}}, []string{"proxy"})
	if len(bundle.Items) == 0 || bundle.Items[0].Path != "src/proxy.go" {
		t.Fatalf("stale vendored map entries should not rank: %+v", bundle.Items)
	}
	for _, item := range bundle.Items {
		if excludedPath(item.Path) {
			t.Fatalf("vendored path leaked into evidence: %s", item.Path)
		}
	}
}

// A truncated or large map used to silence every hit through Scout status.
// Scout advice and injection eligibility are now separate judgements.
func TestTruncatedMapKeepsDirectEvidenceUsable(t *testing.T) {
	files := []File{{Path: "src/auth/rotate.go", Symbols: []Symbol{{Name: "RotateToken", Kind: "function", LineStart: 12, LineEnd: 20}}}}
	for i := 0; i < 1600; i++ {
		files = append(files, File{Path: fmt.Sprintf("pkg/mod%04d/file.go", i)})
	}
	bundle := Evidence(Map{Files: files, Truncated: true}, []string{"rotate"})
	if !bundle.HasDirectEvidence() || bundle.Strength != StrengthDirect {
		t.Fatalf("direct hit in a truncated map must stay usable: %+v", bundle)
	}
	if bundle.Items[0].Path != "src/auth/rotate.go" {
		t.Fatalf("wrong top item: %+v", bundle.Items)
	}
}

// The BM25-metadata-only shape is what produced the unrelated file:line
// guesses; it must never qualify as showable evidence.
func TestMetadataOnlyRelevanceIsNotDirectEvidence(t *testing.T) {
	bundle := Evidence(Map{Files: []File{
		{Path: "src/handler.go", Package: "src", Imports: []string{"net/http/httputil"}, Symbols: []Symbol{{Name: "Serve", Kind: "function", LineStart: 4, LineEnd: 9}}},
	}}, []string{"httputil"})
	if len(bundle.Items) == 0 {
		t.Fatal("import proximity should still rank a candidate; otherwise this guarantee is untested")
	}
	if bundle.HasDirectEvidence() || bundle.Strength != StrengthMetadata {
		t.Fatalf("import-only proximity must not be direct evidence: %+v", bundle)
	}
}

// A term naming an ancestor directory ("src", "internal", "lib" — ordinary
// prompt words) must not mark every file beneath it as a direct hit.
func TestDirectMatchTestsFileNameNotWholePath(t *testing.T) {
	bundle := Evidence(Map{Files: []File{
		{Path: "src/billing/invoice.go", Package: "src", Symbols: []Symbol{{Name: "Total", Kind: "function", LineStart: 3, LineEnd: 8}}},
		{Path: "src/billing/slow_query.go", Package: "src", Symbols: []Symbol{{Name: "Total", Kind: "function", LineStart: 3, LineEnd: 8}}},
	}}, []string{"src", "slow"})
	for _, item := range bundle.Items {
		wantDirect := item.Path == "src/billing/slow_query.go"
		if item.Direct != wantDirect {
			t.Fatalf("%s direct=%t want=%t (ancestor-directory term must not qualify)", item.Path, item.Direct, wantDirect)
		}
	}
}

func TestEmptyEvidenceIsNotShowable(t *testing.T) {
	bundle := Evidence(Map{Files: []File{{Path: "src/handler.go"}}}, []string{"nomatchterm"})
	if len(bundle.Items) != 0 || bundle.Strength != StrengthNone || bundle.HasDirectEvidence() {
		t.Fatalf("empty evidence must not be showable: %+v", bundle)
	}
}

func TestBundleReportsScannedAndRankedCountsSeparately(t *testing.T) {
	bundle := Evidence(Map{Files: []File{
		{Path: "node_modules/leftpad/index.js"},
		{Path: "src/proxy.go", Symbols: []Symbol{{Name: "Proxy", Kind: "function", LineStart: 1, LineEnd: 3}}},
	}}, []string{"proxy"})
	if bundle.FilesScanned != 2 || bundle.FilesRanked != 1 {
		t.Fatalf("scanned/ranked counts must be distinct: scanned=%d ranked=%d", bundle.FilesScanned, bundle.FilesRanked)
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

// The fallback walk is the more expensive path, so git must never be allowed to
// spend the warm budget it needs: a listing that times out at 2s inside a 3s
// window leaves the walk unable to finish and the map fails closed, which is
// exactly the case the fallback exists for.
func TestGitListingLeavesBudgetForTheWalkFallback(t *testing.T) {
	if got := gitListBudget(context.Background()); got != gitListTimeout {
		t.Fatalf("no deadline should use the full cap: %v", got)
	}
	tight, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got := gitListBudget(tight)
	if got > 1500*time.Millisecond {
		t.Fatalf("git may take at most half a 3s warm budget, took %v", got)
	}
	generous, cancelGenerous := context.WithTimeout(context.Background(), time.Minute)
	defer cancelGenerous()
	if got := gitListBudget(generous); got != gitListTimeout {
		t.Fatalf("a generous budget should still cap at %v, got %v", gitListTimeout, got)
	}
}
