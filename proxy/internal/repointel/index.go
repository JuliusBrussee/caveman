// Package repointel builds bounded, deterministic local repository maps and
// task evidence bundles. It performs no network or model calls.
package repointel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/engine/contextwindow"
	"github.com/JuliusBrussee/caveman/proxy/internal/gitsafe"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

const (
	MapSchema    = "caveman.repository-map.v1"
	BundleSchema = "caveman.repository-evidence.v1"
	// ScoutNotConfigured advises that a local Scout might help. It is advice
	// only: whether evidence may be shown to the model is decided by Strength.
	ScoutNotConfigured = "not_started_no_configured_local_scout"
	// StrengthDirect means at least one item matched a task term in its own
	// path or symbol name. Only this strength may be shown to the model.
	StrengthDirect = "direct_path_or_symbol_match"
	// StrengthMetadata means every item ranked on BM25 metadata proximity
	// alone — the shape that produced unrelated vendored-file guesses.
	StrengthMetadata = "metadata_only"
	StrengthNone     = "none"
	// ListingGit and ListingWalk disclose how the file set was obtained. The
	// two can disagree (git omits submodule contents; the walk sees them), so
	// the basis is part of the map and of its content hash.
	ListingGit   = "git_index_and_untracked_excluding_ignored"
	ListingWalk  = "filesystem_walk_dependency_marker_filtered"
	maxFiles     = 20_000
	maxFileBytes = 2 << 20
	maxEvidence  = 8
	// gitListTimeout caps the listing so a slow one degrades to the walk. It is
	// further capped at half the caller's remaining budget (gitListBudget): the
	// fallback walk is the MORE expensive path, so spending most of the warm
	// window on git leaves the walk unable to finish and the map fails closed —
	// exactly the case the fallback exists for.
	gitListTimeout = 2 * time.Second
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
	ListingBasis    string `json:"listing_basis"`
}

type EvidenceItem struct {
	Path      string   `json:"path"`
	LineStart int      `json:"line_start,omitempty"`
	LineEnd   int      `json:"line_end,omitempty"`
	Kind      string   `json:"kind"`
	Reasons   []string `json:"reasons"`
	Score     float64  `json:"score"`
	// Direct records that a task term appears in this file's own path or in
	// one of its symbol names, as opposed to BM25 metadata proximity.
	Direct bool `json:"direct,omitempty"`
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
	FilesRanked     int            `json:"files_ranked"`
	Candidates      int            `json:"candidates"`
	MapTruncated    bool           `json:"map_truncated"`
	Scout           ScoutDecision  `json:"scout"`
	Strength        string         `json:"strength"`
	EvidenceStatus  string         `json:"evidence_status"`
}

// HasDirectEvidence reports whether the bundle earned the right to be shown to
// a model: at least one ranked item matched a task term in its own path or
// symbol name. BM25 metadata proximity alone never qualifies — that is the
// ranking that pointed at unrelated dependency files.
func (b Bundle) HasDirectEvidence() bool { return b.Strength == StrengthDirect && len(b.Items) > 0 }

// DirectOnly drops the items that ranked on metadata proximity alone. A bundle
// qualifies on one direct item, so the rest ride along otherwise — into the
// stored evidence object and behind the ccr:// handle the model is handed,
// which is the same wrong-file claim the block refuses to render.
func (b Bundle) DirectOnly() Bundle {
	items := make([]EvidenceItem, 0, len(b.Items))
	for _, item := range b.Items {
		if item.Direct {
			items = append(items, item)
		}
	}
	b.Items = items
	return b
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
	files, truncated, listingBasis, err := listFiles(ctx, resolved)
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
	contentHash := mapHash(repositoryState, mapped, truncated, parserBasis, listingBasis)
	repoMap := Map{
		Schema: MapSchema, RepositoryState: repositoryState, ContentSHA256: contentHash,
		Files: mapped, Truncated: truncated, ParserBasis: parserBasis, ListingBasis: listingBasis,
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

// gitListFiles is a seam: tests force the filesystem walk by replacing it.
var gitListFiles = gitTrackedFiles

// listFiles prefers git's own ignore rules over any name list we could write:
// a dependency tree the project installs (node_modules, .pixi/envs, .venv,
// a conda prefix under any name) is ignored by that project's .gitignore, so
// git already knows which files are source. Checkouts without git, any git
// failure, and an empty git listing all fall back to a filesystem walk — an
// empty answer is indistinguishable from "this directory is itself ignored by
// an enclosing repository", which must not be reported as a complete map.
func listFiles(ctx context.Context, root string) ([]string, bool, string, error) {
	if files, truncated, ok := gitListFiles(ctx, root); ok && len(files) > 0 {
		return files, truncated, ListingGit, nil
	}
	files, truncated, err := walkFiles(ctx, root)
	return files, truncated, ListingWalk, err
}

// gitListBudget leaves the fallback walk at least as much time as git gets.
func gitListBudget(parent context.Context) time.Duration {
	deadline, ok := parent.Deadline()
	if !ok {
		return gitListTimeout
	}
	if half := time.Until(deadline) / 2; half < gitListTimeout {
		return half
	}
	return gitListTimeout
}

func gitTrackedFiles(parent context.Context, root string) ([]string, bool, bool) {
	ctx, cancel := context.WithTimeout(parent, gitListBudget(parent))
	defer cancel()
	cmd := gitsafe.Command(ctx, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	stdout, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return nil, false, false
	}
	if startErr := cmd.Start(); startErr != nil {
		return nil, false, false
	}
	// git knows which files exist, not which of them are dependency trees a
	// .gitignore forgot, so the same content-based filter the walk uses runs
	// over every listed path, memoised per directory.
	filter := newDirectoryFilter(root)
	files := make([]string, 0, 1024)
	seen := map[string]bool{}
	truncated := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), maxFileBytes)
	scanner.Split(scanNULSeparated)
	for scanner.Scan() {
		relative := filepath.ToSlash(scanner.Text())
		if relative == "" || relative == "." || filepath.IsAbs(relative) || strings.HasPrefix(relative, "../") || seen[relative] {
			continue
		}
		if filter.excludes(relative) {
			continue
		}
		// ls-files also reports submodule gitlinks and symlinks; only regular
		// files of this repository may enter the map.
		info, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(relative)))
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		if len(files) == maxFiles {
			truncated = true
			break
		}
		seen[relative] = true
		files = append(files, relative)
	}
	scanErr := scanner.Err()
	if truncated {
		cancel()
	}
	waitErr := cmd.Wait()
	if scanErr != nil || (waitErr != nil && !truncated) {
		return nil, false, false
	}
	sort.Strings(files)
	return files, truncated, true
}

func scanNULSeparated(data []byte, atEOF bool) (int, []byte, error) {
	if index := bytes.IndexByte(data, 0); index >= 0 {
		return index + 1, data[:index], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func walkFiles(ctx context.Context, root string) ([]string, bool, error) {
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
		// Symlinks and Windows junctions/reparse points are never followed:
		// they loop, and they commonly point at an installed dependency tree.
		if entry.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return nil
		}
		if entry.IsDir() {
			if path != root && excludedDirectory(entry.Name(), path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil || relative == "." || strings.HasPrefix(relative, "..") {
			return nil
		}
		if len(files) == maxFiles {
			truncated = true
			return filepath.SkipAll
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	sort.Strings(files)
	return files, truncated, err
}

// directoryFilter answers "is this path inside a dependency tree" for a list
// of paths, probing each distinct directory at most once. A listing of N files
// costs one decision per directory rather than per file.
type directoryFilter struct {
	root     string
	decision map[string]bool
}

func newDirectoryFilter(root string) *directoryFilter {
	return &directoryFilter{root: root, decision: map[string]bool{}}
}

func (f *directoryFilter) excludes(relative string) bool {
	segments := strings.Split(relative, "/")
	if len(segments) < 2 {
		return false
	}
	current := ""
	for _, segment := range segments[:len(segments)-1] {
		if current == "" {
			current = segment
		} else {
			current += "/" + segment
		}
		decided, known := f.decision[current]
		if !known {
			decided = excludedDirectory(segment, filepath.Join(f.root, filepath.FromSlash(current)))
			f.decision[current] = decided
		}
		if decided {
			return true
		}
	}
	return false
}

// dependencyDirectoryNames are directory names that are never first-party
// source in any ecosystem, so the name alone is sufficient evidence.
var dependencyDirectoryNames = map[string]bool{
	".git": true, "node_modules": true, "bower_components": true, "site-packages": true,
	"__pycache__": true, "__pypackages__": true, ".pnpm-store": true, ".yarn-cache": true,
	".venv": true, "venv": true, "virtualenv": true, ".tox": true, ".nox": true, ".eggs": true,
	".pixi": true, ".conda": true, ".mamba": true, "miniconda3": true, "anaconda3": true, "miniforge3": true,
	".direnv": true, ".bundle": true, ".gradle": true, ".m2": true, ".cargo": true, ".dart_tool": true,
	".pub-cache": true, "elm-stuff": true, ".next": true, ".nuxt": true, ".svelte-kit": true,
	".terraform": true, ".cache": true,
}

// generatedDirectoryManifests are directory names that are dependency or build
// output in one ecosystem and ordinary source in another. `internal/build` and
// a committed Go `vendor/` are first-party; `target/` beside a Cargo.toml is
// not. Each name is excluded only when a sibling manifest proves the ecosystem.
var generatedDirectoryManifests = map[string][]string{
	"vendor":   {"go.mod", "composer.json", "Gemfile"},
	"target":   {"Cargo.toml", "pom.xml"},
	"build":    {"build.gradle", "build.gradle.kts", "CMakeLists.txt", "pyproject.toml", "setup.py"},
	"dist":     {"package.json", "pyproject.toml", "setup.py"},
	"coverage": {"package.json", "pyproject.toml"},
	"pods":     {"Podfile"},
}

// dependencyRootMarkers identify an installed environment by its contents
// rather than its name, which is what makes this scale: a conda prefix, a
// pixi env, or a virtualenv is caught whether it is called `.pixi`, `envs`,
// `default`, or `py314`. It also stops the interpreter's own standard library
// (`<env>/lib/python3.14/...`), which carries no marker of its own.
var dependencyRootMarkers = []string{"conda-meta", "pyvenv.cfg", "site-packages"}

func excludedDirectory(name, path string) bool {
	folded := strings.ToLower(name)
	if dependencyDirectoryNames[folded] {
		return true
	}
	if manifests, ambiguous := generatedDirectoryManifests[folded]; ambiguous && hasSibling(path, manifests) {
		return true
	}
	return hasDependencyMarker(path)
}

func hasSibling(path string, names []string) bool {
	parent := filepath.Dir(path)
	for _, name := range names {
		if info, err := os.Stat(filepath.Join(parent, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func hasDependencyMarker(path string) bool {
	for _, marker := range dependencyRootMarkers {
		if _, err := os.Lstat(filepath.Join(path, marker)); err == nil {
			return true
		}
	}
	return false
}

// excludedPath is the content-blind guard on the exported Evidence entry
// point, which ranks a caller-supplied Map. No caller today feeds it anything
// but a freshly built map that listFiles already filtered, so this fires only
// for a map that reaches ranking some other way — a persisted
// ObjectRepositoryMap replayed later, or a map built by an older version. It
// can only judge unambiguous names, so it is a floor under excludedDirectory,
// never a replacement for it.
func excludedPath(relative string) bool {
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		if dependencyDirectoryNames[strings.ToLower(part)] {
			return true
		}
	}
	return false
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
	cmd := gitsafe.Command(ctx, root, "log", "-n", "50", "--format=", "--name-only", "--", ".")
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
	files := make([]File, 0, len(repoMap.Files))
	for _, file := range repoMap.Files {
		if excludedPath(file.Path) {
			continue
		}
		files = append(files, file)
	}
	query := strings.Join(terms, " ")
	docs := make([]string, len(files))
	for i, file := range files {
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
		score += 0.05 * float64(min(files[i].RecentChanges, 10))
		if files[i].TestFor != "" {
			score += 0.1
		}
		candidates = append(candidates, candidate{i, score})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return files[candidates[i].index].Path < files[candidates[j].index].Path
		}
		return candidates[i].score > candidates[j].score
	})
	items := make([]EvidenceItem, 0, min(maxEvidence, len(candidates)))
	for _, candidate := range candidates[:min(maxEvidence, len(candidates))] {
		file := files[candidate.index]
		item := EvidenceItem{Path: file.Path, Kind: "file", Score: candidate.score, Reasons: evidenceReasons(file, terms)}
		item.Direct = fileNameMatches(file.Path, terms)
		for _, symbol := range file.Symbols {
			if containsAny(strings.ToLower(symbol.Name), terms) {
				item.Kind = symbol.Kind
				item.LineStart = symbol.LineStart
				item.LineEnd = symbol.LineEnd
				item.Direct = true
				break
			}
		}
		items = append(items, item)
	}
	dirs := map[string]bool{}
	direct := 0
	for _, item := range items {
		dirs[filepath.Dir(item.Path)] = true
		if item.Direct {
			direct++
		}
	}
	strength := StrengthNone
	switch {
	case direct > 0:
		strength = StrengthDirect
	case len(items) > 0:
		strength = StrengthMetadata
	}
	// Scout advice is about repository shape, not about whether these items may
	// be shown; the two were conflated, so a truncated map alone silenced good
	// hits. Injection is gated on Strength by the caller.
	large := repoMap.Truncated || len(files) > 1500
	recommended := large && (strength != StrengthDirect || len(items) < 3 || len(dirs) > 4)
	reason := "direct deterministic evidence sufficient"
	status := "not_needed"
	if recommended {
		reason = "repository large/distributed and direct evidence weak; Scout may be net-positive"
		status = ScoutNotConfigured
	}
	return Bundle{
		Schema: BundleSchema, RepositoryState: repoMap.RepositoryState, QueryTerms: terms, Items: items,
		FilesScanned: len(repoMap.Files), FilesRanked: len(files), Candidates: len(candidates), MapTruncated: repoMap.Truncated,
		Scout:          ScoutDecision{Recommended: recommended, Status: status, Reason: reason},
		Strength:       strength,
		EvidenceStatus: "observed_local_repository_metadata",
	}
}

// fileNameMatches deliberately tests the final path component only. Matching
// the whole path made a term naming any ancestor directory ("src", "internal",
// "lib" — ordinary words in a prompt) mark every file beneath it as a direct
// hit, which is no gate at all.
func fileNameMatches(path string, terms []string) bool {
	return containsAny(strings.ToLower(filepath.Base(path)), terms)
}

func evidenceReasons(file File, terms []string) []string {
	var reasons []string
	if fileNameMatches(file.Path, terms) {
		reasons = append(reasons, "file name matches task terms")
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
		// Three characters, matching the hook that produces terms. Direct
		// evidence is a substring test on a file's own name, so a two-letter
		// term ("go", "py", "js") would mark every file of that language a
		// direct hit and hand the gate away.
		if len(value) < 3 || len(value) > 64 || !safeTerm(value) || len(secretRules) > 0 || seen[value] {
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

func mapHash(repositoryState string, files []File, truncated bool, basis, listingBasis string) string {
	hash := sha256.New()
	hash.Write([]byte(repositoryState))
	hash.Write([]byte{0})
	hash.Write([]byte(basis))
	hash.Write([]byte{0})
	hash.Write([]byte(listingBasis))
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
