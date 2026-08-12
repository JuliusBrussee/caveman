// Package repointel builds bounded, deterministic local repository maps and
// task evidence bundles. It performs no network or model calls.
package repointel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/engine/contextwindow"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

const (
	MapSchema    = "caveman.repository-map.v1"
	BundleSchema = "caveman.repository-evidence.v1"
	maxFiles     = 20_000
	maxFileBytes = 2 << 20
	maxEvidence  = 8
)

type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

type File struct {
	Path          string   `json:"path"`
	Language      string   `json:"language,omitempty"`
	Package       string   `json:"package,omitempty"`
	Imports       []string `json:"imports,omitempty"`
	Symbols       []Symbol `json:"symbols,omitempty"`
	TestFor       string   `json:"test_for,omitempty"`
	RecentChanges int      `json:"recent_changes,omitempty"`
	Convention    bool     `json:"convention,omitempty"`
}

type Map struct {
	Schema          string `json:"schema"`
	RepositoryState string `json:"repository_state,omitempty"`
	ContentSHA256   string `json:"content_sha256"`
	Files           []File `json:"files"`
	Truncated       bool   `json:"truncated"`
	ParserBasis     string `json:"parser_basis"`
}

type EvidenceItem struct {
	Path      string   `json:"path"`
	LineStart int      `json:"line_start,omitempty"`
	LineEnd   int      `json:"line_end,omitempty"`
	Kind      string   `json:"kind"`
	Reasons   []string `json:"reasons"`
	Score     float64  `json:"score"`
}

type ScoutDecision struct {
	Recommended bool   `json:"recommended"`
	Status      string `json:"status"`
	Reason      string `json:"reason"`
}

type Bundle struct {
	Schema          string         `json:"schema"`
	RepositoryState string         `json:"repository_state,omitempty"`
	QueryTerms      []string       `json:"query_terms"`
	Items           []EvidenceItem `json:"items"`
	FilesScanned    int            `json:"files_scanned"`
	Candidates      int            `json:"candidates"`
	MapTruncated    bool           `json:"map_truncated"`
	Scout           ScoutDecision  `json:"scout"`
	EvidenceStatus  string         `json:"evidence_status"`
}

type TestImpact struct {
	Schema         string   `json:"schema"`
	ChangedPaths   []string `json:"changed_paths"`
	AffectedTests  []string `json:"affected_tests"`
	Basis          string   `json:"basis"`
	EvidenceStatus string   `json:"evidence_status"`
	Conservative   bool     `json:"conservative"`
}

func Build(ctx context.Context, root, repositoryState string, queryTerms []string) (Map, Bundle, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Map{}, Bundle{}, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return Map{}, Bundle{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return Map{}, Bundle{}, errors.New("repository intelligence: cwd is not a directory")
	}
	files, truncated, err := listFiles(ctx, resolved)
	if err != nil {
		return Map{}, Bundle{}, err
	}
	packages := packageBoundaries(files)
	activity := gitActivity(ctx, resolved)
	mapped := make([]File, 0, len(files))
	for _, relative := range files {
		entry := File{
			Path: relative, Language: languageFor(relative), Package: nearestPackage(relative, packages),
			TestFor: testTarget(relative, files), RecentChanges: activity[relative], Convention: isConvention(relative),
		}
		if entry.Language != "" && !sensitivePath(relative) {
			path := filepath.Join(resolved, filepath.FromSlash(relative))
			if raw, readErr := os.ReadFile(path); readErr == nil && len(raw) <= maxFileBytes && !containsNUL(raw) {
				entry.Imports = scanImports(entry.Language, raw)
				entry.Symbols = scanSymbols(ctx, relative, entry.Language, raw)
			}
		}
		mapped = append(mapped, entry)
	}
	parserBasis := symbolParserBasis()
	contentHash := mapHash(repositoryState, mapped, truncated, parserBasis)
	repoMap := Map{
		Schema: MapSchema, RepositoryState: repositoryState, ContentSHA256: contentHash,
		Files: mapped, Truncated: truncated, ParserBasis: parserBasis,
	}
	bundle := Evidence(repoMap, queryTerms)
	return repoMap, bundle, nil
}

// Evidence ranks task-specific paths from an already-built repository map.
// It remains deterministic and performs no filesystem, network, or model work.
func Evidence(repoMap Map, queryTerms []string) Bundle {
	return buildBundle(repoMap, NormalizeTerms(queryTerms))
}

// ImpactTests returns conservative local test impact from explicit
// test-to-source relationships plus package boundaries. It never claims dynamic
// coverage or safe test-result reuse.
func ImpactTests(repoMap Map, changedPaths []string) TestImpact {
	changed := map[string]bool{}
	packages := map[string]bool{}
	for _, raw := range changedPaths {
		path := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
		if path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
			continue
		}
		changed[path] = true
		for _, file := range repoMap.Files {
			if file.Path == path && file.Package != "" {
				packages[file.Package] = true
			}
		}
	}
	var normalized []string
	for path := range changed {
		normalized = append(normalized, path)
	}
	sort.Strings(normalized)
	seen := map[string]bool{}
	var tests []string
	for _, file := range repoMap.Files {
		if file.TestFor == "" {
			continue
		}
		if changed[file.TestFor] || packages[file.Package] {
			if !seen[file.Path] {
				seen[file.Path] = true
				tests = append(tests, file.Path)
			}
		}
	}
	sort.Strings(tests)
	return TestImpact{
		Schema: "caveman.repository-test-impact.v1", ChangedPaths: normalized, AffectedTests: tests,
		Basis: "direct_test_to_source_plus_same_package_conservative", EvidenceStatus: "observed_local_repository_metadata",
		Conservative: true,
	}
}

func listFiles(ctx context.Context, root string) ([]string, bool, error) {
	files := make([]string, 0, 1024)
	truncated := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if entry.IsDir() {
			if path != root && excludedDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil || relative == "." || strings.HasPrefix(relative, "..") {
			return nil
		}
		files = append(files, filepath.ToSlash(relative))
		if len(files) >= maxFiles {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(files)
	return files, truncated, err
}

func excludedDirectory(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".venv", "venv", "dist", "build", ".next", "target", "coverage", ".cache":
		return true
	default:
		return false
	}
}

func packageBoundaries(files []string) []string {
	seen := map[string]bool{}
	for _, path := range files {
		switch filepath.Base(path) {
		case "go.mod", "package.json", "Cargo.toml", "pyproject.toml", "pom.xml", "build.gradle", "build.gradle.kts":
			dir := filepath.ToSlash(filepath.Dir(path))
			if dir == "." {
				dir = ""
			}
			seen[dir] = true
		}
	}
	out := make([]string, 0, len(seen))
	for dir := range seen {
		out = append(out, dir)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func nearestPackage(path string, packages []string) string {
	dir := filepath.ToSlash(filepath.Dir(path))
	for _, candidate := range packages {
		if candidate == "" || dir == candidate || strings.HasPrefix(dir, candidate+"/") {
			return firstNonEmpty(candidate, ".")
		}
	}
	return ""
}

func languageFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hpp":
		return "cpp"
	default:
		return ""
	}
}

var importPatterns = map[string]*regexp.Regexp{
	"go":         regexp.MustCompile("(?m)^\\s*import\\s+(?:[._A-Za-z0-9]+\\s+)?[\"`]([^\"`]+)[\"`]"),
	"python":     regexp.MustCompile(`(?m)^\s*(?:from\s+([A-Za-z0-9_.]+)\s+import|import\s+([A-Za-z0-9_.]+))`),
	"typescript": regexp.MustCompile(`(?m)^\s*(?:import|export).*?from\s+["']([^"']+)["']|^\s*import\s*["']([^"']+)["']`),
	"javascript": regexp.MustCompile(`(?m)^\s*(?:import|export).*?from\s+["']([^"']+)["']|require\s*\(\s*["']([^"']+)["']\s*\)`),
	"rust":       regexp.MustCompile(`(?m)^\s*(?:use|extern\s+crate)\s+([A-Za-z0-9_:]+)`),
	"java":       regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([A-Za-z0-9_.]+)`),
}

func scanImports(language string, raw []byte) []string {
	pattern := importPatterns[language]
	if pattern == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, match := range pattern.FindAllSubmatch(raw, 500) {
		for _, group := range match[1:] {
			value := strings.TrimSpace(string(group))
			if value != "" && !seen[value] {
				seen[value] = true
				out = append(out, value)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func testTarget(path string, files []string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	candidates := []string{}
	switch {
	case strings.HasSuffix(base, "_test"):
		candidates = append(candidates, strings.TrimSuffix(base, "_test")+ext)
	case strings.HasSuffix(base, ".test"):
		stem := strings.TrimSuffix(base, ".test")
		candidates = append(candidates, stem+ext, stem+".ts", stem+".tsx", stem+".js")
	case strings.HasPrefix(filepath.Base(base), "test_"):
		candidates = append(candidates, filepath.ToSlash(filepath.Join(filepath.Dir(path), strings.TrimPrefix(filepath.Base(base), "test_")+ext)))
	}
	for _, candidate := range candidates {
		index := sort.SearchStrings(files, candidate)
		if index < len(files) && files[index] == candidate {
			return candidate
		}
	}
	return ""
}

func gitActivity(parent context.Context, root string) map[string]int {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "log", "-n", "50", "--format=", "--name-only", "--", ".")
	raw, err := cmd.Output()
	if err != nil {
		return map[string]int{}
	}
	out := map[string]int{}
	for _, line := range strings.Split(string(raw), "\n") {
		path := filepath.ToSlash(strings.TrimSpace(line))
		if path != "" && path != "." && !strings.HasPrefix(path, "../") {
			out[path]++
		}
	}
	return out
}

func buildBundle(repoMap Map, terms []string) Bundle {
	query := strings.Join(terms, " ")
	docs := make([]string, len(repoMap.Files))
	for i, file := range repoMap.Files {
		var symbols []string
		for _, symbol := range file.Symbols {
			symbols = append(symbols, symbol.Name, symbol.Kind)
		}
		docs[i] = strings.Join([]string{file.Path, file.Package, strings.Join(file.Imports, " "), strings.Join(symbols, " "), file.TestFor}, " ")
	}
	scores := contextwindow.BM25(query, docs)
	type candidate struct {
		index int
		score float64
	}
	var candidates []candidate
	for i, score := range scores {
		if score <= 0 {
			continue
		}
		score += 0.05 * float64(min(repoMap.Files[i].RecentChanges, 10))
		if repoMap.Files[i].TestFor != "" {
			score += 0.1
		}
		candidates = append(candidates, candidate{i, score})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return repoMap.Files[candidates[i].index].Path < repoMap.Files[candidates[j].index].Path
		}
		return candidates[i].score > candidates[j].score
	})
	items := make([]EvidenceItem, 0, min(maxEvidence, len(candidates)))
	for _, candidate := range candidates[:min(maxEvidence, len(candidates))] {
		file := repoMap.Files[candidate.index]
		item := EvidenceItem{Path: file.Path, Kind: "file", Score: candidate.score, Reasons: evidenceReasons(file, terms)}
		for _, symbol := range file.Symbols {
			if containsAny(strings.ToLower(symbol.Name), terms) {
				item.Kind = symbol.Kind
				item.LineStart = symbol.LineStart
				item.LineEnd = symbol.LineEnd
				break
			}
		}
		items = append(items, item)
	}
	dirs := map[string]bool{}
	for _, item := range items {
		dirs[filepath.Dir(item.Path)] = true
	}
	recommended := repoMap.Truncated || len(repoMap.Files) > 1500 && (len(items) < 3 || len(dirs) > 4)
	reason := "direct deterministic evidence sufficient"
	status := "not_needed"
	if recommended {
		reason = "repository large/distributed and direct evidence weak; Scout may be net-positive"
		status = "not_started_no_configured_local_scout"
	}
	return Bundle{
		Schema: BundleSchema, RepositoryState: repoMap.RepositoryState, QueryTerms: terms, Items: items,
		FilesScanned: len(repoMap.Files), Candidates: len(candidates), MapTruncated: repoMap.Truncated,
		Scout:          ScoutDecision{Recommended: recommended, Status: status, Reason: reason},
		EvidenceStatus: "observed_local_repository_metadata",
	}
}

func evidenceReasons(file File, terms []string) []string {
	var reasons []string
	if containsAny(strings.ToLower(file.Path), terms) {
		reasons = append(reasons, "path matches task terms")
	}
	for _, symbol := range file.Symbols {
		if containsAny(strings.ToLower(symbol.Name), terms) {
			reasons = append(reasons, "symbol matches task terms")
			break
		}
	}
	if file.TestFor != "" {
		reasons = append(reasons, "test-to-source relationship: "+file.TestFor)
	}
	if file.RecentChanges > 0 {
		reasons = append(reasons, "recent git activity")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "BM25 metadata relevance")
	}
	return reasons
}

// NormalizeTerms bounds query metadata before it can enter CCR or ranking.
func NormalizeTerms(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		_, secretRules := redact.String(value)
		if len(value) < 2 || len(value) > 64 || !safeTerm(value) || len(secretRules) > 0 || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
		if len(out) == 12 {
			break
		}
	}
	return out
}

func safeTerm(value string) bool {
	if strings.Contains(value, "..") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return false
	}
	for _, prefix := range []string{"sk-", "pk-", "rk-", "ghp-", "ghp_", "github_pat-", "github_pat_", "xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-", "akia-", "akia_"} {
		if strings.HasPrefix(value, prefix) {
			return false
		}
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' && char != '.' && char != '/' {
			return false
		}
	}
	return true
}

func mapHash(repositoryState string, files []File, truncated bool, basis string) string {
	hash := sha256.New()
	hash.Write([]byte(repositoryState))
	hash.Write([]byte{0})
	hash.Write([]byte(basis))
	for _, file := range files {
		hash.Write([]byte{0})
		hash.Write([]byte(file.Path))
		for _, symbol := range file.Symbols {
			hash.Write([]byte{0})
			hash.Write([]byte(symbol.Name))
		}
	}
	if truncated {
		hash.Write([]byte("\x00truncated"))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func isConvention(path string) bool {
	base := strings.ToUpper(filepath.Base(path))
	return base == "AGENTS.MD" || base == "CLAUDE.MD" || base == "CONTRIBUTING.MD" || base == "README.MD"
}

func sensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.Contains(base, "secret")
}

func containsNUL(raw []byte) bool { return strings.IndexByte(string(raw), 0) >= 0 }

func containsAny(value string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
