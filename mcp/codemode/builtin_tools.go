package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/browse"
	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/mcp"
)

const (
	defaultBatchMaxLines  = 200
	maxBatchFiles         = 30
	maxBatchFileBytes     = 512 * 1024
	maxSearchFiles        = 5000
	maxSearchArchiveBytes = 2 * 1024 * 1024
	maxCLICaptureBytes    = 16 * 1024 * 1024
	defaultDiffMaxLines   = 200
	defaultCILogLines     = 200
	maxCILogLines         = 2000
	ghCommandTimeout      = 30 * time.Second
)

// cliCappedBuffer captures up to limit bytes while draining further writes so a
// noisy child process (`gh run view --log`) never blocks on a full pipe or OOMs.
type cliCappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cliCappedBuffer) Write(p []byte) (int, error) {
	if rem := c.limit - c.buf.Len(); rem > 0 {
		if len(p) > rem {
			_, _ = c.buf.Write(p[:rem])
		} else {
			_, _ = c.buf.Write(p)
		}
	}
	return len(p), nil
}

// CommandRunner executes a CLI command (used by PR/CI built-ins and injectable
// for deterministic unit tests without network access).
type CommandRunner func(ctx context.Context, dir, name string, args ...string) (stdout, stderr []byte, err error)

func defaultCommandRunner(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	outBuf := &cliCappedBuffer{limit: maxCLICaptureBytes}
	errBuf := &cliCappedBuffer{limit: 1024 * 1024}
	cmd.Stdout = outBuf
	cmd.Stderr = errBuf
	err := cmd.Run()
	return outBuf.buf.Bytes(), errBuf.buf.Bytes(), err
}

// --- Built-in Repo Upstream (batch_read + symbol_outline) -------------------

// NewRepoUpstream builds the built-in `repo` upstream providing multi-file
// bounded reading (`batch_read`) and tree-sitter AST signature extraction
// (`symbol_outline`), strictly sandboxed inside workspaceRoot and backed by CCR.
func NewRepoUpstream(workspaceRoot string, store *ccr.Store, eng *engine.Engine) *InProcessUpstream {
	if eng == nil && store != nil {
		eng = engine.New(store, nil)
	}
	absRoot, _ := filepath.Abs(workspaceRoot)
	if realRoot, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = realRoot
	}

	u := NewInProcessUpstream("repo")

	readOnlyAnnot := &ToolAnnotations{ReadOnlyHint: boolPtr(true), DestructiveHint: boolPtr(false), IdempotentHint: boolPtr(true)}

	u.Register(ToolSpec{
		Name:        "batch_read",
		Description: "Read multiple workspace files in one tool call with line numbers and bounded per-file line caps; large raw files are archived in CCR.",
		Annotations: readOnlyAnnot,
		InputSchema: mcp.ObjectSchema(map[string]any{
			"paths": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Relative workspace paths to read (max 30).",
			},
			"start_line":         map[string]any{"type": "integer", "description": "Optional 1-based start line (default 1)."},
			"end_line":           map[string]any{"type": "integer", "description": "Optional 1-based inclusive end line."},
			"max_lines_per_file": map[string]any{"type": "integer", "description": "Max lines returned per file (default 200)."},
		}, "paths"),
	}, func(ctx context.Context, args map[string]any) (any, error) {
		paths := toStringSlice(args["paths"])
		if len(paths) == 0 {
			return nil, errors.New("paths must be a non-empty array of workspace-relative file paths")
		}
		if len(paths) > maxBatchFiles {
			paths = paths[:maxBatchFiles]
		}
		startLine := toInt(args["start_line"], 1)
		if startLine < 1 {
			startLine = 1
		}
		endLine := toInt(args["end_line"], 0)
		if endLine > 0 && endLine < startLine {
			return nil, fmt.Errorf("end_line (%d) must be >= start_line (%d)", endLine, startLine)
		}
		maxLines := toInt(args["max_lines_per_file"], defaultBatchMaxLines)
		if maxLines <= 0 {
			maxLines = defaultBatchMaxLines
		}

		type fileEntry struct {
			Path           string `json:"path"`
			TotalLines     int    `json:"total_lines,omitempty"`
			ShownRange     string `json:"shown_range,omitempty"`
			Truncated      bool   `json:"truncated,omitempty"`
			RecoveryHandle string `json:"recovery_handle,omitempty"`
			Content        string `json:"content,omitempty"`
			Error          string `json:"error,omitempty"`
		}

		files := make([]fileEntry, 0, len(paths))
		for _, rel := range paths {
			if ctx != nil && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			full, normRel, err := resolveWorkspacePath(absRoot, rel)
			if err != nil {
				files = append(files, fileEntry{Path: rel, Error: err.Error()})
				continue
			}
			info, err := os.Stat(full)
			if err != nil {
				files = append(files, fileEntry{Path: normRel, Error: err.Error()})
				continue
			}
			if info.IsDir() {
				files = append(files, fileEntry{Path: normRel, Error: "path is a directory"})
				continue
			}
			if !info.Mode().IsRegular() {
				files = append(files, fileEntry{Path: normRel, Error: "path is not a regular file"})
				continue
			}
			if info.Size() > maxBatchFileBytes {
				files = append(files, fileEntry{Path: normRel, Error: fmt.Sprintf("file exceeds %d byte cap", maxBatchFileBytes)})
				continue
			}
			raw, err := os.ReadFile(full)
			if err != nil {
				files = append(files, fileEntry{Path: normRel, Error: err.Error()})
				continue
			}
			if bytes.IndexByte(raw, 0) >= 0 {
				files = append(files, fileEntry{Path: normRel, Error: "binary file skipped"})
				continue
			}
			lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
			total := len(lines)
			sIdx := startLine - 1
			if sIdx >= total {
				files = append(files, fileEntry{Path: normRel, TotalLines: total, Content: ""})
				continue
			}
			eIdx := total
			if endLine > 0 && endLine < eIdx {
				eIdx = endLine
			}
			truncated := false
			if eIdx-sIdx > maxLines {
				eIdx = sIdx + maxLines
				truncated = true
			}
			var b strings.Builder
			for i := sIdx; i < eIdx; i++ {
				fmt.Fprintf(&b, "%d|%s\n", i+1, lines[i])
			}
			var handle string
			if truncated && store != nil {
				if h, herr := store.Put(ccr.Recovery{
					ContentType: "code",
					Compressor:  "codemode.batch_read",
					Original:    raw,
				}); herr == nil {
					handle = h
				}
			}
			files = append(files, fileEntry{
				Path:           normRel,
				TotalLines:     total,
				ShownRange:     fmt.Sprintf("%d-%d", sIdx+1, eIdx),
				Truncated:      truncated,
				RecoveryHandle: handle,
				Content:        strings.TrimRight(b.String(), "\n"),
			})
		}
		return map[string]any{"files": files}, nil
	})

	u.Register(ToolSpec{
		Name:        "symbol_outline",
		Description: "Extract compact AST imports, types, and function/method signatures for multiple source files in one call; full originals archived in CCR.",
		Annotations: readOnlyAnnot,
		InputSchema: mcp.ObjectSchema(map[string]any{
			"paths": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Relative workspace source paths to outline (max 30).",
			},
		}, "paths"),
	}, func(ctx context.Context, args map[string]any) (any, error) {
		paths := toStringSlice(args["paths"])
		if len(paths) == 0 {
			return nil, errors.New("paths must be a non-empty array of workspace-relative file paths")
		}
		if len(paths) > maxBatchFiles {
			paths = paths[:maxBatchFiles]
		}
		codeComp, _ := compressors.Default().For("code")

		type outlineEntry struct {
			Path           string `json:"path"`
			TotalLines     int    `json:"total_lines,omitempty"`
			Compressed     bool   `json:"compressed,omitempty"`
			RecoveryHandle string `json:"recovery_handle,omitempty"`
			Outline        string `json:"outline,omitempty"`
			Error          string `json:"error,omitempty"`
		}
		outlines := make([]outlineEntry, 0, len(paths))
		for _, rel := range paths {
			if ctx != nil && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			full, normRel, err := resolveWorkspacePath(absRoot, rel)
			if err != nil {
				outlines = append(outlines, outlineEntry{Path: rel, Error: err.Error()})
				continue
			}
			info, err := os.Stat(full)
			if err != nil {
				outlines = append(outlines, outlineEntry{Path: normRel, Error: err.Error()})
				continue
			}
			if info.IsDir() {
				outlines = append(outlines, outlineEntry{Path: normRel, Error: "path is a directory"})
				continue
			}
			if !info.Mode().IsRegular() {
				outlines = append(outlines, outlineEntry{Path: normRel, Error: "path is not a regular file"})
				continue
			}
			if info.Size() > maxBatchFileBytes {
				outlines = append(outlines, outlineEntry{Path: normRel, Error: fmt.Sprintf("file exceeds %d byte cap", maxBatchFileBytes)})
				continue
			}
			raw, err := os.ReadFile(full)
			if err != nil {
				outlines = append(outlines, outlineEntry{Path: normRel, Error: err.Error()})
				continue
			}
			if bytes.IndexByte(raw, 0) >= 0 {
				outlines = append(outlines, outlineEntry{Path: normRel, Error: "binary file skipped"})
				continue
			}
			totalLines := bytes.Count(raw, []byte{'\n'}) + 1

			var outlined []byte
			var handle string
			compressed := false

			if eng != nil {
				if res, cerr := eng.Compress(raw, engine.Options{Mode: engine.ModeCompress, Type: "code"}); cerr == nil && len(res.Output) < len(raw) {
					outlined = res.Output
					handle = res.RecoveryHandle
					compressed = true
				}
			}
			if !compressed && codeComp != nil {
				if out, ok := codeComp.Compress(raw); ok && len(out) < len(raw) {
					outlined = out
					compressed = true
					if store != nil {
						if h, herr := store.Put(ccr.Recovery{
							ContentType: "code",
							Compressor:  "code",
							Original:    raw,
						}); herr == nil {
							handle = h
						}
					}
				}
			}
			if !compressed {
				outlined = raw
			}
			outlines = append(outlines, outlineEntry{
				Path:           normRel,
				TotalLines:     totalLines,
				Compressed:     compressed,
				RecoveryHandle: handle,
				Outline:        string(outlined),
			})
		}
		return map[string]any{"files": outlines}, nil
	})

	u.Register(ToolSpec{
		Name:        "search_code",
		Description: "Search workspace source files for a literal or regex pattern and return compact path-grouped matches (skipping .git/node_modules/binaries); full output archived in CCR when truncated.",
		Annotations: readOnlyAnnot,
		InputSchema: mcp.ObjectSchema(map[string]any{
			"query":       mcp.StringProp("Literal text or regex pattern to search for."),
			"regex":       map[string]any{"type": "boolean", "description": "Interpret query as a Go regular expression (default false = case-insensitive substring)."},
			"glob":        mcp.StringProp("Optional filename glob filter (e.g. '*.go', '*.ts')."),
			"subpath":     mcp.StringProp("Optional relative subdirectory inside the workspace."),
			"max_matches": map[string]any{"type": "integer", "description": "Max matching lines returned (default 80, max 500)."},
		}, "query"),
	}, func(ctx context.Context, args map[string]any) (any, error) {
		query := strings.TrimSpace(toString(args["query"]))
		if query == "" {
			return nil, errors.New("query is required")
		}
		useRegex, _ := args["regex"].(bool)
		var re *regexp.Regexp
		if useRegex {
			var err error
			re, err = regexp.Compile(query)
			if err != nil {
				return nil, fmt.Errorf("invalid regex: %w", err)
			}
		}
		glob := strings.TrimSpace(toString(args["glob"]))
		subpath := strings.TrimSpace(toString(args["subpath"]))
		searchRoot := absRoot
		if subpath != "" {
			resolved, _, err := resolveWorkspacePath(absRoot, subpath)
			if err != nil {
				return nil, err
			}
			searchRoot = resolved
		}
		maxMatches := toInt(args["max_matches"], 80)
		if maxMatches <= 0 {
			maxMatches = 80
		}
		if maxMatches > 500 {
			maxMatches = 500
		}

		lowerQ := strings.ToLower(query)
		grouped := map[string][]string{}
		var allLines []string
		allLinesBytes := 0
		scannedFiles := 0
		totalMatches := 0
		truncated := false
		walkErr := filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			// Never follow symlinks during search — prevents symlink escape
			// outside workspace root (e.g. repo/leak.txt -> /etc/passwd).
			if d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if name == ".git" || name == "node_modules" || name == "dist" || name == "vendor" || name == ".next" || name == "target" {
					return filepath.SkipDir
				}
				return nil
			}
			if glob != "" {
				if matched, _ := filepath.Match(glob, name); !matched {
					return nil
				}
			}
			scannedFiles++
			if scannedFiles > maxSearchFiles {
				truncated = true
				return fs.SkipAll
			}
			info, ierr := d.Info()
			if ierr != nil || !info.Mode().IsRegular() || info.Size() > maxBatchFileBytes || info.Size() == 0 {
				return nil
			}
			resolved, normRel, rerr := resolveWorkspacePath(absRoot, path)
			if rerr != nil {
				return nil
			}
			raw, rerr := os.ReadFile(resolved)
			if rerr != nil || bytes.IndexByte(raw, 0) >= 0 {
				return nil // skip binary files
			}
			lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
			for idx, line := range lines {
				matched := false
				if re != nil {
					matched = re.MatchString(line)
				} else {
					matched = strings.Contains(strings.ToLower(line), lowerQ)
				}
				if !matched {
					continue
				}
				totalMatches++
				trimmed := truncateRunes(strings.TrimRight(line, " \t"), 220)
				entry := fmt.Sprintf("L%d: %s", idx+1, trimmed)
				fullLine := normRel + ":" + entry
				if allLinesBytes+len(fullLine)+1 <= maxSearchArchiveBytes {
					allLines = append(allLines, fullLine)
					allLinesBytes += len(fullLine) + 1
				}
				if totalMatches <= maxMatches {
					grouped[normRel] = append(grouped[normRel], entry)
				} else {
					truncated = true
				}
			}
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
			return nil, walkErr
		}

		files := make([]string, 0, len(grouped))
		for f := range grouped {
			files = append(files, f)
		}
		sort.Strings(files)
		ordered := make([]map[string]any, 0, len(files))
		for _, f := range files {
			ordered = append(ordered, map[string]any{
				"file":    f,
				"matches": grouped[f],
			})
		}
		out := map[string]any{
			"total_matches": totalMatches,
			"truncated":     truncated,
			"files":         ordered,
		}
		if truncated && store != nil {
			if h, herr := store.Put(ccr.Recovery{
				ContentType: "text",
				Compressor:  "codemode.search_code",
				Original:    []byte(strings.Join(allLines, "\n")),
			}); herr == nil && h != "" {
				out["recovery_handle"] = h
			}
		}
		return out, nil
	})

	return u
}

func resolveWorkspacePath(absRoot, rel string) (string, string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", "", errors.New("empty path")
	}
	var candidate string
	if filepath.IsAbs(rel) {
		candidate = filepath.Clean(rel)
	} else {
		candidate = filepath.Join(absRoot, rel)
	}
	if realCand, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = realCand
	}
	relCheck, err := filepath.Rel(absRoot, candidate)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(os.PathSeparator)) || filepath.IsAbs(relCheck) {
		return "", "", fmt.Errorf("path %q escapes workspace root", rel)
	}
	return candidate, filepath.ToSlash(relCheck), nil
}

// --- Built-in PR & CI Upstream (pr_read + ci_logs via gh CLI) ---------------

// NewPRUpstream builds the built-in `pr` upstream offering T3-Code-style
// bounded PR state/checks/comments/diff inspection (`pr_read`) and failing CI
// log retrieval (`ci_logs`) backed by `gh` CLI + CCR archival.
func NewPRUpstream(workspaceRoot string, store *ccr.Store, eng *engine.Engine, runner CommandRunner) *InProcessUpstream {
	if runner == nil {
		runner = defaultCommandRunner
	}
	if eng == nil && store != nil {
		eng = engine.New(store, nil)
	}
	u := NewInProcessUpstream("pr")

	prReadOnly := &ToolAnnotations{ReadOnlyHint: boolPtr(true), DestructiveHint: boolPtr(false), IdempotentHint: boolPtr(true)}

	u.Register(ToolSpec{
		Name:        "pr_read",
		Description: "Read bounded PR summary, checks, review comments, or diff via gh CLI (only requested sections fetched; raw diff archived in CCR).",
		Annotations: prReadOnly,
		InputSchema: mcp.ObjectSchema(map[string]any{
			"number":         map[string]any{"type": "integer", "description": "PR number (defaults to current branch's PR)."},
			"repo":           mcp.StringProp("Optional OWNER/REPO override."),
			"sections":       map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"summary", "checks", "comments", "diff"}}, "description": "Sections to fetch (default ['summary','checks'])."},
			"max_diff_lines": map[string]any{"type": "integer", "description": "Max diff lines when 'diff' section is requested (default 200)."},
		}),
	}, func(ctx context.Context, args map[string]any) (any, error) {
		runCtx, cancel := context.WithTimeout(ctx, ghCommandTimeout)
		defer cancel()

		number := toInt(args["number"], 0)
		repo := strings.TrimSpace(toString(args["repo"]))
		sections := toStringSlice(args["sections"])
		if len(sections) == 0 {
			sections = []string{"summary", "checks"}
		}
		want := map[string]bool{}
		for _, s := range sections {
			k := strings.ToLower(strings.TrimSpace(s))
			switch k {
			case "summary", "checks", "comments", "diff":
				want[k] = true
			case "":
			default:
				return nil, fmt.Errorf("unknown pr_read section %q (valid: summary, checks, comments, diff)", s)
			}
		}
		if len(want) == 0 {
			want["summary"] = true
			want["checks"] = true
		}
		maxDiffLines := toInt(args["max_diff_lines"], defaultDiffMaxLines)
		if maxDiffLines <= 0 {
			maxDiffLines = defaultDiffMaxLines
		}

		prRef := []string{}
		if number > 0 {
			prRef = append(prRef, strconv.Itoa(number))
		}
		repoFlags := []string{}
		if repo != "" {
			if strings.HasPrefix(repo, "-") || !strings.Contains(repo, "/") {
				return nil, fmt.Errorf("invalid repo %q: expected OWNER/REPO", repo)
			}
			repoFlags = append(repoFlags, "--repo", repo)
		}

		result := map[string]any{}

		// Fetch summary / comments in one `gh pr view --json` call when either is requested.
		var viewFields []string
		if want["summary"] {
			viewFields = append(viewFields, "number", "title", "state", "isDraft", "url", "baseRefName", "headRefName", "headRefOid", "mergeable", "reviewDecision", "author", "additions", "deletions", "changedFiles")
		}
		if want["comments"] {
			viewFields = append(viewFields, "comments", "reviews")
		}
		if len(viewFields) > 0 {
			cmdArgs := append([]string{"pr", "view"}, prRef...)
			cmdArgs = append(cmdArgs, repoFlags...)
			cmdArgs = append(cmdArgs, "--json", strings.Join(viewFields, ","))
			stdout, stderr, err := runner(runCtx, workspaceRoot, "gh", cmdArgs...)
			if err != nil {
				return nil, fmt.Errorf("gh pr view: %v (%s)", err, strings.TrimSpace(string(stderr)))
			}
			var parsed map[string]any
			if err := json.Unmarshal(stdout, &parsed); err != nil {
				return nil, fmt.Errorf("decode gh pr view json: %w", err)
			}
			if want["summary"] {
				summary := map[string]any{}
				for _, k := range []string{"number", "title", "state", "isDraft", "url", "baseRefName", "headRefName", "headRefOid", "mergeable", "reviewDecision", "author", "additions", "deletions", "changedFiles"} {
					if v, ok := parsed[k]; ok {
						summary[k] = v
					}
				}
				result["summary"] = summary
			}
			if want["comments"] {
				result["comments"] = compactPRComments(parsed["comments"], parsed["reviews"], store)
			}
		}

		if want["checks"] {
			cmdArgs := append([]string{"pr", "checks"}, prRef...)
			cmdArgs = append(cmdArgs, repoFlags...)
			cmdArgs = append(cmdArgs, "--json", "name,state,bucket,workflow,link,description")
			stdout, stderr, err := runner(runCtx, workspaceRoot, "gh", cmdArgs...)
			// `gh pr checks` exits non-zero when checks fail even though stdout has valid JSON!
			if len(bytes.TrimSpace(stdout)) == 0 && err != nil {
				return nil, fmt.Errorf("gh pr checks: %v (%s)", err, strings.TrimSpace(string(stderr)))
			}
			var checks any
			if jerr := json.Unmarshal(stdout, &checks); jerr == nil {
				result["checks"] = checks
			} else {
				result["checks_text"] = strings.TrimSpace(string(stdout))
			}
		}

		if want["diff"] {
			cmdArgs := append([]string{"pr", "diff"}, prRef...)
			cmdArgs = append(cmdArgs, repoFlags...)
			stdout, stderr, err := runner(runCtx, workspaceRoot, "gh", cmdArgs...)
			if err != nil {
				return nil, fmt.Errorf("gh pr diff: %v (%s)", err, strings.TrimSpace(string(stderr)))
			}
			lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
			totalLines := len(lines)
			truncated := false
			shown := lines
			if len(shown) > maxDiffLines {
				shown = shown[:maxDiffLines]
				truncated = true
			}
			diffObj := map[string]any{
				"total_lines": totalLines,
				"truncated":   truncated,
				"text":        strings.Join(shown, "\n"),
			}
			if truncated && store != nil {
				if h, herr := store.Put(ccr.Recovery{
					ContentType: "diff",
					Compressor:  "codemode.pr_read",
					Original:    stdout,
				}); herr == nil {
					diffObj["recovery_handle"] = h
				}
			}
			result["diff"] = diffObj
		}

		return result, nil
	})

	u.Register(ToolSpec{
		Name:        "ci_logs",
		Description: "Read bounded failing CI check logs via gh CLI, compressing test/log noise and archiving the full raw log in CCR.",
		Annotations: prReadOnly,
		InputSchema: mcp.ObjectSchema(map[string]any{
			"run_id":       map[string]any{"type": "integer", "description": "GitHub Actions run ID (if omitted, resolves latest failing run for the PR/branch)."},
			"number":       map[string]any{"type": "integer", "description": "Optional PR number used to locate its failing Actions run."},
			"repo":         mcp.StringProp("Optional OWNER/REPO override."),
			"tail_lines":   map[string]any{"type": "integer", "description": "Max trailing lines to return (default 200, max 2000)."},
			"failing_only": map[string]any{"type": "boolean", "description": "Fetch only failing step logs via --log-failed (default true)."},
		}),
	}, func(ctx context.Context, args map[string]any) (any, error) {
		runCtx, cancel := context.WithTimeout(ctx, ghCommandTimeout)
		defer cancel()

		runID := toInt(args["run_id"], 0)
		prNumber := toInt(args["number"], 0)
		repo := strings.TrimSpace(toString(args["repo"]))
		tailLines := toInt(args["tail_lines"], defaultCILogLines)
		if tailLines <= 0 {
			tailLines = defaultCILogLines
		}
		if tailLines > maxCILogLines {
			tailLines = maxCILogLines
		}
		failingOnly := true
		if v, ok := args["failing_only"].(bool); ok {
			failingOnly = v
		}

		repoFlags := []string{}
		if repo != "" {
			if strings.HasPrefix(repo, "-") || !strings.Contains(repo, "/") {
				return nil, fmt.Errorf("invalid repo %q: expected OWNER/REPO", repo)
			}
			repoFlags = append(repoFlags, "--repo", repo)
		}

		listRepoFlags := append([]string(nil), repoFlags...)
		if runID <= 0 && prNumber > 0 {
			// Resolve the PR's failing Actions run directly from its checks list,
			// falling back to its head branch runs so we never grab another PR's run.
			chkArgs := append([]string{"pr", "checks", strconv.Itoa(prNumber)}, repoFlags...)
			chkArgs = append(chkArgs, "--json", "name,state,bucket,link")
			if chkOut, _, _ := runner(runCtx, workspaceRoot, "gh", chkArgs...); len(bytes.TrimSpace(chkOut)) > 0 {
				var checks []struct {
					Bucket string `json:"bucket"`
					State  string `json:"state"`
					Link   string `json:"link"`
				}
				if json.Unmarshal(chkOut, &checks) == nil {
					for _, c := range checks {
						if strings.EqualFold(c.Bucket, "fail") || strings.EqualFold(c.State, "FAILURE") {
							if id := extractActionsRunID(c.Link); id > 0 {
								runID = id
								break
							}
						}
					}
					if runID <= 0 {
						for _, c := range checks {
							if id := extractActionsRunID(c.Link); id > 0 {
								runID = id
								break
							}
						}
					}
				}
			}
			if runID <= 0 {
				viewArgs := append([]string{"pr", "view", strconv.Itoa(prNumber)}, repoFlags...)
				viewArgs = append(viewArgs, "--json", "headRefName")
				if vOut, _, verr := runner(runCtx, workspaceRoot, "gh", viewArgs...); verr == nil {
					var prInfo struct {
						HeadRefName string `json:"headRefName"`
					}
					if json.Unmarshal(vOut, &prInfo) == nil && prInfo.HeadRefName != "" {
						listRepoFlags = append(listRepoFlags, "--branch", prInfo.HeadRefName)
					}
				}
			}
		}

		if runID <= 0 {
			// Discover latest failing (or latest) workflow run.
			listArgs := append([]string{"run", "list", "--limit", "10", "--json", "databaseId,conclusion,status,name,headBranch"}, listRepoFlags...)
			stdout, stderr, err := runner(runCtx, workspaceRoot, "gh", listArgs...)
			if err != nil {
				return nil, fmt.Errorf("gh run list: %v (%s)", err, strings.TrimSpace(string(stderr)))
			}
			var runs []struct {
				DatabaseID int    `json:"databaseId"`
				Conclusion string `json:"conclusion"`
				Status     string `json:"status"`
			}
			if err := json.Unmarshal(stdout, &runs); err != nil || len(runs) == 0 {
				return nil, errors.New("no workflow runs found")
			}
			for _, r := range runs {
				if strings.EqualFold(r.Conclusion, "failure") {
					runID = r.DatabaseID
					break
				}
			}
			if runID <= 0 {
				runID = runs[0].DatabaseID
			}
		}

		logFlag := "--log-failed"
		if !failingOnly {
			logFlag = "--log"
		}
		viewArgs := append([]string{"run", "view", strconv.Itoa(runID), logFlag}, repoFlags...)
		stdout, stderr, err := runner(runCtx, workspaceRoot, "gh", viewArgs...)
		if err != nil && len(bytes.TrimSpace(stdout)) == 0 {
			return nil, fmt.Errorf("gh run view %d: %v (%s)", runID, err, strings.TrimSpace(string(stderr)))
		}

		raw := stdout
		rawTotalLines := 0
		if len(raw) > 0 {
			rawTotalLines = bytes.Count(bytes.TrimRight(raw, "\n"), []byte{'\n'}) + 1
		}
		// Compress through Engine when it yields a smaller structured view, then
		// apply the bounded tail_lines cap. Reuse Engine's CCR handle to avoid
		// double-storing the raw log.
		rendered := string(raw)
		compressed := false
		var handle string
		if eng != nil && len(raw) > 0 {
			if res, cerr := eng.Compress(raw, engine.Options{Mode: engine.ModeCompress}); cerr == nil && len(res.Output) < len(raw) {
				rendered = string(res.Output)
				compressed = true
				handle = res.RecoveryHandle
			}
		}
		lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
		truncated := false
		if len(lines) > tailLines {
			lines = lines[len(lines)-tailLines:]
			truncated = true
		}
		if handle == "" && store != nil && (truncated || len(raw) >= minArchiveBytes) {
			if h, herr := store.Put(ccr.Recovery{
				ContentType: "log",
				Compressor:  "codemode.ci_logs",
				Original:    raw,
			}); herr == nil {
				handle = h
			}
		}
		out := map[string]any{
			"run_id":      runID,
			"total_lines": rawTotalLines,
			"truncated":   truncated,
			"compressed":  compressed,
			"log":         strings.Join(lines, "\n"),
		}
		if handle != "" {
			out["recovery_handle"] = handle
		}
		return out, nil
	})

	return u
}

var actionsRunIDPattern = regexp.MustCompile(`/actions/runs/([0-9]+)`)

func extractActionsRunID(link string) int {
	m := actionsRunIDPattern.FindStringSubmatch(link)
	if len(m) < 2 {
		return 0
	}
	id, err := strconv.Atoi(m[1])
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

func compactPRComments(rawComments, rawReviews any, store *ccr.Store) map[string]any {
	const maxCommentRunes = 1200
	out := map[string]any{}
	anyTruncated := false
	if arr, ok := rawComments.([]any); ok {
		items := make([]map[string]any, 0, len(arr))
		for _, c := range arr {
			m, ok := c.(map[string]any)
			if !ok {
				continue
			}
			author := ""
			if a, ok := m["author"].(map[string]any); ok {
				author = toString(a["login"])
			}
			fullBody := strings.TrimSpace(toString(m["body"]))
			clipped := truncateRunes(fullBody, maxCommentRunes)
			entry := map[string]any{
				"author": author,
				"body":   clipped,
			}
			if clipped != fullBody {
				entry["truncated"] = true
				anyTruncated = true
			}
			items = append(items, entry)
		}
		out["issue_comments"] = items
	}
	if arr, ok := rawReviews.([]any); ok {
		items := make([]map[string]any, 0, len(arr))
		for _, r := range arr {
			m, ok := r.(map[string]any)
			if !ok {
				continue
			}
			author := ""
			if a, ok := m["author"].(map[string]any); ok {
				author = toString(a["login"])
			}
			fullBody := strings.TrimSpace(toString(m["body"]))
			clipped := truncateRunes(fullBody, maxCommentRunes)
			entry := map[string]any{
				"author": author,
				"state":  toString(m["state"]),
				"body":   clipped,
			}
			if clipped != fullBody {
				entry["truncated"] = true
				anyTruncated = true
			}
			items = append(items, entry)
		}
		out["reviews"] = items
	}
	if anyTruncated && store != nil {
		if rawBytes, err := json.Marshal(map[string]any{"comments": rawComments, "reviews": rawReviews}); err == nil {
			if h, herr := store.Put(ccr.Recovery{
				ContentType: "json",
				Compressor:  "codemode.pr_comments",
				Original:    rawBytes,
			}); herr == nil && h != "" {
				out["recovery_handle"] = h
			}
		}
	}
	return out
}

// --- Built-in Browse Upstream (lazy wrapper around browse.Session) ----------

// BrowseUpstream exposes Caveman's AX-tree browser tools (`browser_snapshot`,
// `browser_act`, `browser_eval`, `browser_recover`) inside CodeMode while
// deferring Chrome startup until the first actual browser tool call.
type BrowseUpstream struct {
	eng           *engine.Engine
	log           *slog.Logger
	driverFactory func(context.Context) (browse.Driver, error)

	mu              sync.Mutex
	lifecycleCancel context.CancelFunc
	session         *browse.Session
	handlers        map[string]mcp.Handler
	specs           []ToolSpec
}

// NewBrowseUpstream constructs a lazy `browse` upstream wrapping `browse/`.
// When driverFactory is nil, the first tool call launches headless Chrome via
// browse.NewCDPDriver.
func NewBrowseUpstream(eng *engine.Engine, log *slog.Logger, driverFactory func(context.Context) (browse.Driver, error)) *BrowseUpstream {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if driverFactory == nil {
		driverFactory = func(ctx context.Context) (browse.Driver, error) {
			return browse.NewCDPDriver(ctx, browse.CDPOptions{Headless: true})
		}
	}
	// Derive tool metadata from a nil-driver Session so ListTools never boots
	// Chrome while staying 100% in sync with browse.BrowserTools.
	stubTools := browse.BrowserTools(browse.NewSession(eng, nil, log))
	specs := make([]ToolSpec, 0, len(stubTools))
	for _, t := range stubTools {
		ro := t.Name == browse.ToolSnapshot || t.Name == browse.ToolRecover
		specs = append(specs, ToolSpec{
			Server:      "browse",
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Annotations: &ToolAnnotations{
				ReadOnlyHint:    boolPtr(ro),
				DestructiveHint: boolPtr(!ro),
				IdempotentHint:  boolPtr(ro),
			},
		})
	}
	return &BrowseUpstream{
		eng:           eng,
		log:           log,
		driverFactory: driverFactory,
		specs:         specs,
	}
}

func (b *BrowseUpstream) Name() string { return "browse" }

func (b *BrowseUpstream) ListTools(_ context.Context) ([]ToolSpec, error) {
	out := make([]ToolSpec, len(b.specs))
	copy(out, b.specs)
	return out, nil
}

func (b *BrowseUpstream) ensureSession(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session != nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	// Own a dedicated session-lifetime context for the headless Chrome driver so
	// canceling the first CallTool's per-call timeout never kills Chrome for
	// subsequent calls (canceled only in Close()).
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	drv, err := b.driverFactory(lifeCtx)
	if err != nil {
		lifeCancel()
		return fmt.Errorf("start browse driver: %w", err)
	}
	sess := browse.NewSession(b.eng, drv, b.log)
	handlers := map[string]mcp.Handler{}
	for _, t := range browse.BrowserTools(sess) {
		handlers[t.Name] = t.Handler
	}
	b.lifecycleCancel = lifeCancel
	b.session = sess
	b.handlers = handlers
	return nil
}

func (b *BrowseUpstream) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	if err := b.ensureSession(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	h, ok := b.handlers[name]
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown tool %q on server %q", name, b.Name())
	}
	if args == nil {
		args = map[string]any{}
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	res := h(rawArgs)
	var texts []string
	for _, c := range res.Content {
		if c.Text != "" {
			texts = append(texts, c.Text)
		}
	}
	joined := strings.Join(texts, "\n")
	if res.IsError {
		if joined == "" {
			joined = "browse tool failed"
		}
		return nil, errors.New(joined)
	}
	return decodeMCPTextPayload(joined), nil
}

func (b *BrowseUpstream) Close() error {
	b.mu.Lock()
	sess := b.session
	cancel := b.lifecycleCancel
	b.session = nil
	b.handlers = nil
	b.lifecycleCancel = nil
	b.mu.Unlock()
	var err error
	if sess != nil {
		err = sess.Close()
	}
	if cancel != nil {
		cancel()
	}
	return err
}

// --- small arg coercion helpers ---------------------------------------------

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func toInt(v any, fallback int) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i
		}
	}
	return fallback
}

func toStringSlice(v any) []string {
	switch arr := v.(type) {
	case []string:
		return arr
	case []any:
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			if s := strings.TrimSpace(toString(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if s := strings.TrimSpace(arr); s != "" {
			return []string{s}
		}
	}
	return nil
}
