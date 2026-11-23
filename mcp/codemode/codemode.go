// Package codemode provides a full-stack MCP Code-Mode Gateway built on
// Caveman's MCP server, deterministic BM25 tool discovery, CCR recovery store,
// and compression engine.
//
// Instead of injecting 30–100+ raw MCP tool schemas (15k–60k tokens) into
// every LLM turn and dumping megabytes of raw intermediate MCP JSON into
// conversation history across N round-trips, a Gateway multiplexes any number
// of downstream MCP servers behind 3 compact meta-tools (~550 tokens total):
//
//   - codemode_search_tools: BM25 search returning compact TypeScript-style
//     signatures on demand (progressive disclosure).
//   - codemode_execute: deterministic, zero-shell sandboxed multi-step MCP
//     pipeline (call, for_each fan-out, filter, pick field-projection, sort,
//     slice, count, tail_lines, grep_lines) that chains downstream MCP calls
//     and filters/projects huge payloads BEFORE they enter the LLM context,
//     archiving raw intermediate payloads in CCR and compressing large outputs.
//   - caveman_retrieve: byte-exact recovery of any CCR handle stored by
//     codemode_execute or the Caveman proxy.
package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/contextwindow"
	"github.com/JuliusBrussee/caveman/mcp"
)

const (
	maxPipelineSteps   = 32
	maxFanOutItems     = 50
	defaultSearchLimit = 8
	maxSearchLimit     = 25
	defaultMaxOutChars = 16000
	minArchiveBytes    = 1024
)

// ToolAnnotations mirrors standard MCP tool safety annotations.
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

func boolPtr(v bool) *bool { return &v }

// ToolSpec describes one downstream MCP tool exposed by an Upstream.
type ToolSpec struct {
	Server      string           `json:"server"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema map[string]any   `json:"inputSchema,omitempty"`
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
}

// mutatingToolVerbs are the name tokens that mark a downstream MCP tool as
// state-changing.
//
// A verb missing here is not a missing prompt: IsReadOnlyTool grants the
// read-only surface to a name whose first or last token is a read verb, so any
// name shaped `<read verb>_and_<unlisted write verb>` (e.g. `find_and_replace`,
// `search_replace`, `list_and_append`) is promoted to read-only and
// `codemode_execute` runs it under read-only annotations with no host approval
// prompt. Every write verb a catalog can spell as an exact token belongs here.
var mutatingToolVerbs = []string{
	"create", "update", "delete", "remove", "destroy", "drop", "write", "edit",
	"patch", "put", "post", "set", "merge", "close", "reopen", "approve", "cancel",
	"kill", "stop", "start", "launch", "run", "exec", "eval", "act", "click", "type",
	"push", "commit", "send", "respond", "review", "comment", "deploy", "mutate",
	"insert", "upsert", "rename", "archive", "link", "unlink", "schedule", "rerun",
	"replace", "append", "prepend", "overwrite", "save", "upload", "move", "copy",
	"clone", "toggle", "enable", "disable", "install", "uninstall", "reset",
	"restore", "purge", "truncate",
}

// IsReadOnlyTool classifies whether a downstream MCP tool is safe for read-only
// execution (`codemode_execute`) vs requiring `codemode_mutate` so the host
// coding agent's native approval prompt still guards state-changing calls.
func IsReadOnlyTool(spec ToolSpec) bool {
	if spec.Annotations != nil {
		if spec.Annotations.DestructiveHint != nil && *spec.Annotations.DestructiveHint {
			return false
		}
		if spec.Annotations.ReadOnlyHint != nil {
			return *spec.Annotations.ReadOnlyHint
		}
	}
	// Split identifier into tokens across _, -, ., /, and camelCase boundaries so
	// mixed names like "get_and_delete-issue" or "readFileWrite" never fail open.
	tokens := splitToolNameTokens(spec.Name)
	for _, verb := range mutatingToolVerbs {
		for _, tok := range tokens {
			if tok == verb {
				return false
			}
		}
	}
	if len(tokens) > 0 {
		first, last := tokens[0], tokens[len(tokens)-1]
		for _, ro := range []string{
			"get", "list", "read", "search", "find", "describe", "inspect", "status",
			"logs", "log", "outline", "snapshot", "fetch", "query", "summary", "count",
			"view", "check", "checks", "info", "show", "diff", "recover", "retrieve",
			"capabilities", "models",
		} {
			if first == ro || last == ro {
				return true
			}
		}
	}
	// Fail closed: unknown tools default to mutating so write prompts are never bypassed.
	return false
}

func splitToolNameTokens(name string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	var prev rune
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			prev = 0
			continue
		}
		if unicode.IsUpper(r) && prev != 0 && unicode.IsLower(prev) {
			flush()
		}
		cur = append(cur, r)
		prev = r
	}
	flush()
	return out
}

// Upstream is a downstream MCP server (in-process or external stdio/HTTP).
type Upstream interface {
	Name() string
	ListTools(ctx context.Context) ([]ToolSpec, error)
	CallTool(ctx context.Context, name string, args map[string]any) (any, error)
}

// InProcessUpstream is a lightweight Upstream backed by Go tool handlers
// (for built-in structured tools or unit/E2E testing).
type InProcessUpstream struct {
	name     string
	tools    []ToolSpec
	handlers map[string]func(ctx context.Context, args map[string]any) (any, error)
}

// NewInProcessUpstream creates an Upstream with the given server name.
func NewInProcessUpstream(name string) *InProcessUpstream {
	return &InProcessUpstream{
		name:     name,
		handlers: map[string]func(ctx context.Context, args map[string]any) (any, error){},
	}
}

// Register adds one tool to an InProcessUpstream.
func (u *InProcessUpstream) Register(spec ToolSpec, fn func(ctx context.Context, args map[string]any) (any, error)) {
	spec.Server = u.name
	u.tools = append(u.tools, spec)
	u.handlers[spec.Name] = fn
}

func (u *InProcessUpstream) Name() string { return u.name }

func (u *InProcessUpstream) ListTools(_ context.Context) ([]ToolSpec, error) {
	out := make([]ToolSpec, len(u.tools))
	copy(out, u.tools)
	return out, nil
}

func (u *InProcessUpstream) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	fn, ok := u.handlers[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q on server %q", name, u.name)
	}
	return fn(ctx, args)
}

// Gateway multiplexes downstream MCP upstreams behind Caveman's Code-Mode
// meta-tools with CCR archival and Engine compression.
type Gateway struct {
	mu                   sync.RWMutex
	upstreams            map[string]Upstream
	catalog              []ToolSpec
	byKey                map[string]ToolSpec // "server.tool" -> ToolSpec
	eng                  *engine.Engine
	store                *ccr.Store
	log                  *slog.Logger
	jsEnabled            bool
	jsMemoryLimit        uintptr
	allowWritesInExecute bool
}

// SetAllowWritesInExecute controls whether `codemode_execute` may call
// state-changing tools directly (false by default: read-only tools run in
// `codemode_execute`, mutating tools require `codemode_mutate`).
func (g *Gateway) SetAllowWritesInExecute(allow bool) {
	g.mu.Lock()
	g.allowWritesInExecute = allow
	g.mu.Unlock()
}

// NewGateway constructs a Code-Mode gateway backed by store (optional) and eng
// (optional; created from store if nil).
func NewGateway(store *ccr.Store, eng *engine.Engine, log *slog.Logger) *Gateway {
	if eng == nil && store != nil {
		eng = engine.New(store, nil)
	}
	return &Gateway{
		upstreams: map[string]Upstream{},
		byKey:     map[string]ToolSpec{},
		eng:       eng,
		store:     store,
		log:       log,
		jsEnabled: true,
	}
}

// SetJSRunnerEnabled toggles the embedded QuickJS ES2023 sandbox for
// codemode_execute / codemode_mutate's `code` parameter.
func (g *Gateway) SetJSRunnerEnabled(enabled bool) {
	g.mu.Lock()
	g.jsEnabled = enabled
	g.mu.Unlock()
}

// SetJSMemoryLimit overrides the QuickJS heap memory limit in bytes (default 32 MiB).
func (g *Gateway) SetJSMemoryLimit(limit uintptr) {
	g.mu.Lock()
	g.jsMemoryLimit = limit
	g.mu.Unlock()
}

// Close closes any registered upstreams that implement io.Closer.
func (g *Gateway) Close() error {
	g.mu.RLock()
	ups := make([]Upstream, 0, len(g.upstreams))
	for _, u := range g.upstreams {
		ups = append(ups, u)
	}
	g.mu.RUnlock()
	var firstErr error
	for _, u := range ups {
		if closer, ok := u.(io.Closer); ok {
			if err := closer.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// AddUpstream registers a downstream MCP server and indexes its tool catalog.
func (g *Gateway) AddUpstream(ctx context.Context, u Upstream) error {
	if u == nil || strings.TrimSpace(u.Name()) == "" {
		return fmt.Errorf("upstream name is required")
	}
	tools, err := u.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("list tools from %q: %w", u.Name(), err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.upstreams[u.Name()] = u
	// Rebuild deterministic sorted catalog.
	g.byKey = map[string]ToolSpec{}
	var all []ToolSpec
	for name, up := range g.upstreams {
		var list []ToolSpec
		if name == u.Name() {
			list = tools
		} else {
			list, _ = up.ListTools(ctx)
		}
		for _, t := range list {
			t.Server = name
			all = append(all, t)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Server != all[j].Server {
			return all[i].Server < all[j].Server
		}
		return all[i].Name < all[j].Name
	})
	g.catalog = all
	for _, t := range all {
		g.byKey[t.Server+"."+t.Name] = t
	}
	return nil
}

// CatalogSize returns the total number of downstream tools indexed.
func (g *Gateway) CatalogSize() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.catalog)
}

// Tools returns the compact MCP meta-tools exposed by this gateway with
// standard MCP read/write annotations so coding-agent harnesses can safely
// auto-approve read-only queries while preserving permission prompts on writes.
func (g *Gateway) Tools() []mcp.Tool {
	g.mu.RLock()
	writesInExec := g.allowWritesInExecute
	g.mu.RUnlock()
	roAnnot := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true}
	mutAnnot := map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false}
	execAnnot := roAnnot
	execDesc := "Run a read-only multi-step sandboxed MCP pipeline (`steps`) or embedded QuickJS async JS snippet (`code` with `await mcp.server.tool(args)`) in one turn, filtering/projecting payloads before returning compact output. Large raw responses are archived in CCR. Use codemode_mutate for state-changing calls."
	if writesInExec {
		execAnnot = mutAnnot
		execDesc = "Run a multi-step sandboxed MCP pipeline (`steps`) or embedded QuickJS async JS snippet (`code` with `await mcp.server.tool(args)`) in one turn (write calls enabled via --allow-writes-in-execute). Large raw responses are archived in CCR."
	}
	tools := []mcp.Tool{
		{
			Name:        "codemode_search_tools",
			Description: "Search connected MCP tools by keyword and return compact typed signatures plus read/write access tags.",
			InputSchema: mcp.ObjectSchema(map[string]any{
				"query":  mcp.StringProp("Keywords describing the action or resource (e.g. 'pull request ci logs', 'search code')."),
				"server": mcp.StringProp("Optional MCP server filter."),
				"limit":  map[string]any{"type": "integer", "description": "Max tools to return (default 8, max 25)."},
			}, "query"),
			Annotations: roAnnot,
			Handler:     g.handleSearchTools,
		},
		{
			Name:        "codemode_execute",
			Description: execDesc,
			InputSchema: mcp.ObjectSchema(map[string]any{
				"steps": map[string]any{
					"type":        "array",
					"description": "Ordered pipeline steps (zero-shell mode). Ops: 'call' (tool:'server.name', args, optional for_each), 'filter', 'pick', 'sort', 'slice', 'count', 'tail_lines', 'grep_lines'.",
					"items":       map[string]any{"type": "object"},
				},
				"code":             mcp.StringProp("Optional QuickJS async JS body (alternative to steps). Call tools with `await mcp.<server>.<tool>(args)` and `return` a compact JSON value."),
				"return_step":      mcp.StringProp("Step id whose value to return in steps mode (defaults to last step)."),
				"max_output_chars": map[string]any{"type": "integer", "description": "Optional max output chars before engine compression (default 16000)."},
				"timeout_ms":       map[string]any{"type": "integer", "description": "Optional timeout ms for code mode (default 30000, max 60000)."},
			}),
			Annotations: execAnnot,
			Handler:     g.handleExecute,
		},
		{
			Name:        "codemode_mutate",
			Description: "Execute state-changing / write MCP tool call(s) via single `tool`+`args`, multi-step `steps`, or embedded QuickJS async JS `code`. Separate from read-only codemode_execute so host permission prompts guard writes.",
			InputSchema: mcp.ObjectSchema(map[string]any{
				"tool":             mcp.StringProp("Single mutating tool name formatted as `server.tool_name` (shortcut instead of steps/code)."),
				"args":             map[string]any{"type": "object", "description": "Arguments for single-tool mode."},
				"steps":            map[string]any{"type": "array", "description": "Optional multi-step pipeline with writes permitted.", "items": map[string]any{"type": "object"}},
				"code":             mcp.StringProp("Optional QuickJS async JS body with writes permitted."),
				"return_step":      mcp.StringProp("Step id to return in steps mode."),
				"max_output_chars": map[string]any{"type": "integer", "description": "Optional max output chars (default 16000)."},
				"timeout_ms":       map[string]any{"type": "integer", "description": "Optional timeout ms for code mode."},
			}),
			Annotations: mutAnnot,
			Handler:     g.handleMutate,
		},
	}
	if g.eng != nil {
		tools = append(tools, mcp.Tool{
			Name:            "caveman_retrieve",
			Description:     "Retrieve exact original bytes for a <<ccr:...>> handle archived by codemode_execute or the proxy.",
			InputSchema:     mcp.ObjectSchema(map[string]any{"recovery_handle": mcp.StringProp("The CCR recovery handle."), "query": mcp.StringProp("Optional query to narrow records.")}, "recovery_handle"),
			Annotations:     roAnnot,
			ExemptResultCap: true,
			Handler:         g.handleRetrieve,
		})
	}
	return tools
}

// NewServer builds an *mcp.Server serving this Gateway's compact meta-tools.
func NewServer(gw *Gateway, log *slog.Logger) *mcp.Server {
	return mcp.NewServer("caveman-codemode", gw.Tools(), log)
}

// --- codemode_search_tools --------------------------------------------------

type searchArgs struct {
	Query  string `json:"query"`
	Server string `json:"server,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type compactToolMatch struct {
	Tool        string `json:"tool"`
	Access      string `json:"access"` // "read" (codemode_execute) or "write" (codemode_mutate)
	Signature   string `json:"signature"`
	Description string `json:"description"`
}

func (g *Gateway) handleSearchTools(raw json.RawMessage) mcp.ToolResult {
	var args searchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ToolError("cave_invalid_params", "invalid search_tools arguments")
	}
	limit := args.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	g.mu.RLock()
	candidates := make([]ToolSpec, 0, len(g.catalog))
	for _, t := range g.catalog {
		if args.Server != "" && !strings.EqualFold(t.Server, args.Server) {
			continue
		}
		candidates = append(candidates, t)
	}
	totalTools := len(g.catalog)
	g.mu.RUnlock()

	if len(candidates) == 0 {
		return mcp.ToolText(map[string]any{"total_tools": totalTools, "matches": []compactToolMatch{}})
	}

	docs := make([]string, len(candidates))
	for i, t := range candidates {
		docs[i] = toolSearchDoc(t)
	}
	scores := contextwindow.BM25(args.Query, docs)
	type scored struct {
		idx   int
		score float64
	}
	ranked := make([]scored, 0, len(candidates))
	for i, s := range scores {
		if s > 0 || strings.TrimSpace(args.Query) == "" {
			ranked = append(ranked, scored{idx: i, score: s})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].idx < ranked[j].idx
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	matches := make([]compactToolMatch, 0, len(ranked))
	for _, r := range ranked {
		t := candidates[r.idx]
		access := "write"
		if IsReadOnlyTool(t) {
			access = "read"
		}
		matches = append(matches, compactToolMatch{
			Tool:        t.Server + "." + t.Name,
			Access:      access,
			Signature:   CompactSignature(t),
			Description: firstSentence(t.Description, 140),
		})
	}
	return mcp.ToolText(map[string]any{
		"total_tools": totalTools,
		"matches":     matches,
	})
}

func toolSearchDoc(t ToolSpec) string {
	var b strings.Builder
	b.WriteString(t.Server)
	b.WriteByte(' ')
	spaced := strings.ReplaceAll(t.Name, "_", " ")
	b.WriteString(spaced)
	b.WriteByte(' ')
	b.WriteString(t.Name)
	b.WriteByte(' ')
	b.WriteString(t.Description)
	if props, ok := t.InputSchema["properties"].(map[string]any); ok {
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteByte(' ')
			b.WriteString(k)
		}
	}
	// Light plural stemming so singular queries ("ticket", "issue", "log") match
	// plural tool names/descriptions ("list_tickets", "ci_logs").
	base := b.String()
	for _, tok := range strings.Fields(base) {
		clean := strings.Trim(strings.ToLower(tok), ".,:;()[]{}\"'`")
		if len(clean) > 3 && strings.HasSuffix(clean, "s") && !strings.HasSuffix(clean, "ss") {
			b.WriteByte(' ')
			b.WriteString(clean[:len(clean)-1])
		}
	}
	return b.String()
}

// CompactSignature renders a tool's JSON Schema as a concise TypeScript-like
// call signature (`server.name({a: string, b?: integer})`), ~15x smaller than
// raw JSON Schema.
func CompactSignature(t ToolSpec) string {
	props, _ := t.InputSchema["properties"].(map[string]any)
	reqSet := map[string]bool{}
	switch req := t.InputSchema["required"].(type) {
	case []string:
		for _, r := range req {
			reqSet[r] = true
		}
	case []any:
		for _, r := range req {
			if s, ok := r.(string); ok {
				reqSet[s] = true
			}
		}
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		pMap, _ := props[k].(map[string]any)
		typ := schemaTypeSummary(pMap)
		opt := "?"
		if reqSet[k] {
			opt = ""
		}
		parts = append(parts, k+opt+": "+typ)
	}
	return fmt.Sprintf("%s.%s({%s})", t.Server, t.Name, strings.Join(parts, ", "))
}

func schemaTypeSummary(m map[string]any) string {
	if m == nil {
		return "any"
	}
	if enum, ok := m["enum"].([]any); ok && len(enum) > 0 && len(enum) <= 5 {
		vals := make([]string, 0, len(enum))
		for _, v := range enum {
			vals = append(vals, fmt.Sprintf("%q", fmt.Sprint(v)))
		}
		return strings.Join(vals, "|")
	}
	if enum, ok := m["enum"].([]string); ok && len(enum) > 0 && len(enum) <= 5 {
		vals := make([]string, 0, len(enum))
		for _, v := range enum {
			vals = append(vals, fmt.Sprintf("%q", v))
		}
		return strings.Join(vals, "|")
	}
	typ, _ := m["type"].(string)
	if typ == "array" {
		if items, ok := m["items"].(map[string]any); ok {
			return schemaTypeSummary(items) + "[]"
		}
		return "any[]"
	}
	if typ == "" {
		return "any"
	}
	return typ
}

func firstSentence(s string, maxLen int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if idx := strings.Index(s, ". "); idx > 0 && idx < maxLen {
		return s[:idx+1]
	}
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

// --- codemode_execute -------------------------------------------------------

type executeArgs struct {
	Tool           string         `json:"tool,omitempty"`
	Args           map[string]any `json:"args,omitempty"`
	Steps          []PipelineStep `json:"steps,omitempty"`
	Code           string         `json:"code,omitempty"`
	ReturnStep     string         `json:"return_step,omitempty"`
	MaxOutputChars int            `json:"max_output_chars,omitempty"`
	TimeoutMS      int            `json:"timeout_ms,omitempty"`
}

// PipelineStep is one deterministic operation inside codemode_execute.
type PipelineStep struct {
	ID       string         `json:"id"`
	Op       string         `json:"op"` // call, filter, pick, sort, slice, count, tail_lines, grep_lines
	Tool     string         `json:"tool,omitempty"`
	Args     map[string]any `json:"args,omitempty"`
	ForEach  string         `json:"for_each,omitempty"` // "$step" or "$step.field"
	From     string         `json:"from,omitempty"`     // "$step" or "$step.field"
	Where    *FilterPred    `json:"where,omitempty"`
	Fields   []string       `json:"fields,omitempty"`
	By       string         `json:"by,omitempty"`
	Desc     bool           `json:"desc,omitempty"`
	Start    int            `json:"start,omitempty"`
	End      int            `json:"end,omitempty"`
	Lines    int            `json:"lines,omitempty"`
	Pattern  string         `json:"pattern,omitempty"`
	MaxLines int            `json:"max_lines,omitempty"`
}

// FilterPred matches elements in a JSON array during a "filter" step.
type FilterPred struct {
	Field    string `json:"field,omitempty"`
	Equals   any    `json:"equals,omitempty"`
	NotEqual any    `json:"not_equals,omitempty"`
	Contains string `json:"contains,omitempty"`
	In       []any  `json:"in,omitempty"`
}

type writeNotAllowedError struct {
	tool string
}

func (e writeNotAllowedError) Error() string {
	return fmt.Sprintf("tool %q is state-changing; call codemode_mutate instead of read-only codemode_execute", e.tool)
}

func (g *Gateway) handleExecute(raw json.RawMessage) mcp.ToolResult {
	g.mu.RLock()
	allowWrites := g.allowWritesInExecute
	g.mu.RUnlock()
	return g.runExecute(raw, allowWrites)
}

func (g *Gateway) handleMutate(raw json.RawMessage) mcp.ToolResult {
	return g.runExecute(raw, true)
}

func (g *Gateway) runExecute(raw json.RawMessage, allowWrites bool) mcp.ToolResult {
	var args executeArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ToolError("cave_invalid_params", "invalid codemode execution arguments")
	}
	maxOut := args.MaxOutputChars
	if maxOut <= 0 {
		maxOut = defaultMaxOutChars
	}
	// Single-tool shortcut on codemode_mutate: `{tool: "server.op", args: {...}}`.
	if strings.TrimSpace(args.Tool) != "" && len(args.Steps) == 0 && strings.TrimSpace(args.Code) == "" {
		args.Steps = []PipelineStep{{
			ID:   "call_1",
			Op:   "call",
			Tool: strings.TrimSpace(args.Tool),
			Args: args.Args,
		}}
	}
	hasSteps := len(args.Steps) > 0
	hasCode := strings.TrimSpace(args.Code) != ""
	if hasSteps && hasCode {
		return mcp.ToolError("cave_invalid_params", "provide either steps or code, not both")
	}
	if hasCode {
		return g.handleExecuteJS(args, maxOut, allowWrites)
	}
	if !hasSteps {
		return mcp.ToolError("cave_invalid_params", "steps, code, or tool must be provided")
	}
	if len(args.Steps) > maxPipelineSteps {
		return mcp.ToolError("cave_limit_exceeded", fmt.Sprintf("max %d steps per execution", maxPipelineSteps))
	}

	timeout := defaultJSTimeout
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
		if timeout > maxJSTimeout {
			timeout = maxJSTimeout
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	env := map[string]any{}
	ccrHandles := map[string]string{}
	toolCalls := 0
	var lastID string

	for i, step := range args.Steps {
		id := strings.TrimSpace(step.ID)
		if id == "" {
			id = fmt.Sprintf("step_%d", i+1)
		}
		lastID = id
		var val any
		var err error
		switch step.Op {
		case "call":
			if step.ForEach != "" {
				val, err = g.execFanOut(ctx, step, env, ccrHandles, &toolCalls, allowWrites)
			} else {
				resolved, rerr := resolveValue(step.Args, env)
				if rerr != nil {
					return mcp.ToolError("cave_ref_error", fmt.Sprintf("step %s: %v", id, rerr))
				}
				callArgs, _ := resolved.(map[string]any)
				val, err = g.invokeUpstream(ctx, step.Tool, callArgs, id, ccrHandles, allowWrites)
				toolCalls++
			}
		case "filter":
			src, rerr := resolvePath(step.From, env)
			if rerr != nil {
				return mcp.ToolError("cave_ref_error", fmt.Sprintf("step %s: %v", id, rerr))
			}
			val, err = opFilter(src, step.Where)
		case "pick":
			src, rerr := resolvePath(step.From, env)
			if rerr != nil {
				return mcp.ToolError("cave_ref_error", fmt.Sprintf("step %s: %v", id, rerr))
			}
			val = opPick(src, step.Fields)
		case "sort":
			src, rerr := resolvePath(step.From, env)
			if rerr != nil {
				return mcp.ToolError("cave_ref_error", fmt.Sprintf("step %s: %v", id, rerr))
			}
			val, err = opSort(src, step.By, step.Desc)
		case "slice":
			src, rerr := resolvePath(step.From, env)
			if rerr != nil {
				return mcp.ToolError("cave_ref_error", fmt.Sprintf("step %s: %v", id, rerr))
			}
			val, err = opSlice(src, step.Start, step.End)
		case "count":
			src, rerr := resolvePath(step.From, env)
			if rerr != nil {
				return mcp.ToolError("cave_ref_error", fmt.Sprintf("step %s: %v", id, rerr))
			}
			val = opCount(src)
		case "tail_lines":
			src, rerr := resolvePath(step.From, env)
			if rerr != nil {
				return mcp.ToolError("cave_ref_error", fmt.Sprintf("step %s: %v", id, rerr))
			}
			val = opTailLines(src, step.Lines)
		case "grep_lines":
			src, rerr := resolvePath(step.From, env)
			if rerr != nil {
				return mcp.ToolError("cave_ref_error", fmt.Sprintf("step %s: %v", id, rerr))
			}
			val, err = opGrepLines(src, step.Pattern, step.MaxLines)
		default:
			return mcp.ToolError("cave_unknown_op", fmt.Sprintf("step %s: unsupported op %q", id, step.Op))
		}
		if err != nil {
			if _, isWriteErr := err.(writeNotAllowedError); isWriteErr {
				return mcp.ToolError("cave_write_not_allowed", fmt.Sprintf("step %s: %v", id, err))
			}
			return mcp.ToolError("cave_exec_failed", fmt.Sprintf("step %s: %v", id, err))
		}
		env[id] = val
	}

	retID := strings.TrimSpace(args.ReturnStep)
	if retID == "" {
		retID = lastID
	}
	resultVal, ok := env[retID]
	if !ok {
		return mcp.ToolError("cave_ref_error", fmt.Sprintf("return_step %q not found", retID))
	}

	out := map[string]any{
		"result":     resultVal,
		"tool_calls": toolCalls,
	}
	if len(ccrHandles) > 0 {
		out["ccr_handles"] = ccrHandles
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return mcp.ToolError("cave_marshal_failed", "could not encode execution result")
	}
	return g.finalizeExecutionOutput(encoded, maxOut)
}

// finalizeExecutionOutput compresses an oversized computed execution result
// ONLY after its full original JSON is archived in CCR and the recovery handle
// is disclosed in the output. Otherwise fails closed toward the original bytes.
func (g *Gateway) finalizeExecutionOutput(encoded []byte, maxOut int) mcp.ToolResult {
	if len(encoded) > maxOut && g.eng != nil {
		if comp, cerr := g.eng.Compress(encoded, engine.Options{Mode: engine.ModeCompress}); cerr == nil && len(comp.Output) < len(encoded) {
			handle := comp.RecoveryHandle
			if handle == "" && g.store != nil {
				if h, serr := g.store.Put(ccr.Recovery{
					ContentType: "json",
					Compressor:  "codemode.execute",
					Original:    encoded,
				}); serr == nil {
					handle = h
				}
			}
			if handle != "" {
				outText := string(comp.Output)
				if !strings.Contains(outText, handle) {
					outText += "\n<<ccr:" + handle + ">>"
				}
				if len(outText) < len(encoded) {
					return mcp.ToolRawText(outText)
				}
			}
		}
	}
	return mcp.ToolRawText(string(encoded))
}

func (g *Gateway) invokeUpstream(ctx context.Context, fullTool string, args map[string]any, stepID string, ccrHandles map[string]string, allowWrites bool) (any, error) {
	dot := strings.IndexByte(fullTool, '.')
	if dot <= 0 || dot >= len(fullTool)-1 {
		return nil, fmt.Errorf("tool %q must be formatted as server.tool_name", fullTool)
	}
	serverName, toolName := fullTool[:dot], fullTool[dot+1:]
	g.mu.RLock()
	up, ok := g.upstreams[serverName]
	spec, hasSpec := g.byKey[fullTool]
	g.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown MCP server %q", serverName)
	}
	if !allowWrites {
		if !hasSpec {
			spec = ToolSpec{Server: serverName, Name: toolName}
		}
		if !IsReadOnlyTool(spec) {
			return nil, writeNotAllowedError{tool: fullTool}
		}
	}
	res, err := up.CallTool(ctx, toolName, args)
	if err != nil {
		return nil, err
	}
	// Normalize via JSON round-trip so downstream structs become map[string]any / []any
	// and archive large raw payloads in CCR for lossless recoverability.
	raw, merr := json.Marshal(res)
	if merr != nil {
		return res, nil
	}
	if g.store != nil && len(raw) >= minArchiveBytes {
		if handle, serr := g.store.Put(ccr.Recovery{
			ContentType: "json",
			Compressor:  "codemode",
			Original:    raw,
		}); serr == nil && handle != "" {
			ccrHandles[stepID] = handle
		}
	}
	var normalized any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&normalized) != nil {
		return res, nil
	}
	return normalized, nil
}

func (g *Gateway) execFanOut(ctx context.Context, step PipelineStep, env map[string]any, ccrHandles map[string]string, toolCalls *int, allowWrites bool) (any, error) {
	src, err := resolvePath(step.ForEach, env)
	if err != nil {
		return nil, err
	}
	arr, ok := src.([]any)
	if !ok {
		return nil, fmt.Errorf("for_each target %q is not an array", step.ForEach)
	}
	if len(arr) > maxFanOutItems {
		arr = arr[:maxFanOutItems]
	}
	results := make([]any, 0, len(arr))
	for i, item := range arr {
		childEnv := make(map[string]any, len(env))
		for k, v := range env {
			childEnv[k] = v
		}
		childEnv["item"] = item
		resolved, rerr := resolveValue(step.Args, childEnv)
		if rerr != nil {
			return nil, rerr
		}
		callArgs, _ := resolved.(map[string]any)
		val, cerr := g.invokeUpstream(ctx, step.Tool, callArgs, fmt.Sprintf("%s[%d]", step.ID, i), ccrHandles, allowWrites)
		*toolCalls++
		if cerr != nil {
			return nil, cerr
		}
		results = append(results, val)
	}
	return results, nil
}

func (g *Gateway) handleRetrieve(raw json.RawMessage) mcp.ToolResult {
	var args struct {
		Handle string `json:"recovery_handle"`
		Query  string `json:"query,omitempty"`
	}
	if err := json.Unmarshal(raw, &args); err != nil || strings.TrimSpace(args.Handle) == "" {
		return mcp.ToolError("cave_invalid_params", "recovery_handle is required")
	}
	handle := normalizeCCRHandle(args.Handle)
	var data []byte
	var err error
	if strings.TrimSpace(args.Query) != "" {
		data, err = g.eng.RetrieveQuery(handle, args.Query)
	} else {
		data, err = g.eng.Retrieve(handle)
	}
	if err != nil {
		return mcp.ToolError("cave_recovery_not_found", err.Error())
	}
	return mcp.ToolRawText(string(data))
}

func normalizeCCRHandle(handle string) string {
	handle = strings.TrimSpace(handle)
	if strings.HasPrefix(handle, "<<") && strings.HasSuffix(handle, ">>") {
		handle = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(handle, "<<"), ">>"))
	}
	if rest, ok := strings.CutPrefix(handle, "ccr://"); ok {
		return strings.Trim(strings.TrimSpace(rest), "/")
	}
	if rest, ok := strings.CutPrefix(handle, "ccr:"); ok {
		return strings.TrimSpace(rest)
	}
	return handle
}

// --- expression & transform helpers -----------------------------------------

func resolveValue(v any, env map[string]any) (any, error) {
	switch node := v.(type) {
	case string:
		if strings.HasPrefix(node, "$") {
			return resolvePath(node, env)
		}
		return node, nil
	case map[string]any:
		out := make(map[string]any, len(node))
		for k, child := range node {
			rv, err := resolveValue(child, env)
			if err != nil {
				return nil, err
			}
			out[k] = rv
		}
		return out, nil
	case []any:
		out := make([]any, len(node))
		for i, child := range node {
			rv, err := resolveValue(child, env)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	default:
		return v, nil
	}
}

func resolvePath(expr string, env map[string]any) (any, error) {
	expr = strings.TrimSpace(expr)
	if !strings.HasPrefix(expr, "$") {
		return nil, fmt.Errorf("reference %q must start with '$'", expr)
	}
	parts := splitPathExpr(expr[1:])
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty reference %q", expr)
	}
	cur, ok := env[parts[0]]
	if !ok {
		return nil, fmt.Errorf("unknown variable $%s", parts[0])
	}
	for _, seg := range parts[1:] {
		if idx, err := strconv.Atoi(seg); err == nil {
			arr, ok := cur.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return nil, fmt.Errorf("index %d out of range in %q", idx, expr)
			}
			cur = arr[idx]
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot read field %q in %q", seg, expr)
		}
		next, exists := m[seg]
		if !exists {
			return nil, fmt.Errorf("field %q missing in %q", seg, expr)
		}
		cur = next
	}
	return cur, nil
}

func splitPathExpr(s string) []string {
	s = strings.ReplaceAll(s, "[", ".")
	s = strings.ReplaceAll(s, "]", "")
	raw := strings.Split(s, ".")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func opFilter(src any, pred *FilterPred) (any, error) {
	arr, ok := src.([]any)
	if !ok {
		return nil, fmt.Errorf("filter requires an array")
	}
	if pred == nil {
		return arr, nil
	}
	out := make([]any, 0, len(arr))
	for _, item := range arr {
		val := item
		if pred.Field != "" {
			val = lookupField(item, pred.Field)
		}
		if matchesPred(val, pred) {
			out = append(out, item)
		}
	}
	return out, nil
}

func lookupField(item any, path string) any {
	cur := item
	for _, seg := range splitPathExpr(path) {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[seg]
	}
	return cur
}

func matchesPred(val any, pred *FilterPred) bool {
	s := fmt.Sprint(val)
	if pred.Equals != nil && fmt.Sprint(pred.Equals) != s {
		return false
	}
	if pred.NotEqual != nil && fmt.Sprint(pred.NotEqual) == s {
		return false
	}
	if pred.Contains != "" && !strings.Contains(strings.ToLower(s), strings.ToLower(pred.Contains)) {
		return false
	}
	if len(pred.In) > 0 {
		found := false
		for _, candidate := range pred.In {
			if fmt.Sprint(candidate) == s {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func opPick(src any, fields []string) any {
	pickObj := func(obj any) any {
		m, ok := obj.(map[string]any)
		if !ok {
			return obj
		}
		out := make(map[string]any, len(fields))
		for _, f := range fields {
			if v := lookupField(m, f); v != nil {
				out[f] = v
			}
		}
		return out
	}
	if arr, ok := src.([]any); ok {
		out := make([]any, len(arr))
		for i, item := range arr {
			out[i] = pickObj(item)
		}
		return out
	}
	return pickObj(src)
}

func opSort(src any, by string, desc bool) (any, error) {
	arr, ok := src.([]any)
	if !ok {
		return nil, fmt.Errorf("sort requires an array")
	}
	cp := append([]any(nil), arr...)
	sort.SliceStable(cp, func(i, j int) bool {
		a, b := cp[i], cp[j]
		if by != "" {
			a, b = lookupField(a, by), lookupField(b, by)
		}
		af, aok := toFloat(a)
		bf, bok := toFloat(b)
		if aok && bok {
			if desc {
				return af > bf
			}
			return af < bf
		}
		as, bs := fmt.Sprint(a), fmt.Sprint(b)
		if desc {
			return as > bs
		}
		return as < bs
	})
	return cp, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

func opSlice(src any, start, end int) (any, error) {
	arr, ok := src.([]any)
	if !ok {
		return nil, fmt.Errorf("slice requires an array")
	}
	if start < 0 {
		start = 0
	}
	if start > len(arr) {
		start = len(arr)
	}
	if end <= 0 || end > len(arr) {
		end = len(arr)
	}
	if end < start {
		end = start
	}
	return arr[start:end], nil
}

func opCount(src any) int {
	switch v := src.(type) {
	case []any:
		return len(v)
	case map[string]any:
		return len(v)
	case string:
		return len(v)
	default:
		return 0
	}
}

func stringifyForLines(src any) string {
	switch v := src.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = stringifyForLines(item)
		}
		return strings.Join(parts, "\n")
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func opTailLines(src any, n int) string {
	if n <= 0 {
		n = 40
	}
	text := stringifyForLines(src)
	lines := strings.Split(text, "\n")
	if len(lines) <= n {
		return text
	}
	return fmt.Sprintf("… %d earlier lines omitted …\n%s", len(lines)-n, strings.Join(lines[len(lines)-n:], "\n"))
}

func opGrepLines(src any, pattern string, maxLines int) (string, error) {
	if maxLines <= 0 {
		maxLines = 60
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return "", fmt.Errorf("invalid grep pattern %q: %w", pattern, err)
	}
	lines := strings.Split(stringifyForLines(src), "\n")
	var matched []string
	for i, line := range lines {
		if re.MatchString(line) {
			matched = append(matched, fmt.Sprintf("L%d: %s", i+1, line))
			if len(matched) >= maxLines {
				break
			}
		}
	}
	return strings.Join(matched, "\n"), nil
}
