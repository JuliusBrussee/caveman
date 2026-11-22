package codemode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	stdioProtocolVersion = "2024-11-05"
	stdioInitTimeout     = 15 * time.Second
	stdioCallTimeout     = 45 * time.Second
	stdioMaxLineBytes    = 32 << 20 // 32 MiB
)

// ServerConfig describes one external MCP server (stdio command OR remote
// Streamable HTTP URL) to bridge behind the Code-Mode gateway.
type ServerConfig struct {
	Command      string            `json:"command,omitempty"`
	Args         []string          `json:"args,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Cwd          string            `json:"cwd,omitempty"`
	URL          string            `json:"url,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	IncludeTools []string          `json:"includeTools,omitempty"`
	ExcludeTools []string          `json:"excludeTools,omitempty"`
}

// Valid reports whether ServerConfig names either a stdio command or a remote HTTP(S) URL.
func (c ServerConfig) Valid() bool {
	if strings.TrimSpace(c.Command) != "" {
		return true
	}
	u := strings.TrimSpace(c.URL)
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// StdioUpstream wraps an external MCP server child process speaking
// newline-delimited JSON-RPC 2.0 on stdin/stdout.
type StdioUpstream struct {
	name   string
	cfg    ServerConfig
	log    *slog.Logger
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *cappedBuffer

	mu       sync.Mutex // serializes JSON-RPC request/response frames
	nextID   atomic.Int64
	closed   atomic.Bool
	tools    []ToolSpec
	hasTools bool
}

// StartStdioUpstream spawns the configured MCP server, performs the MCP
// initialize + notifications/initialized handshake, and returns a live Upstream.
func StartStdioUpstream(ctx context.Context, name string, cfg ServerConfig, log *slog.Logger) (*StdioUpstream, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("stdio upstream name is required")
	}
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("stdio upstream %q: command is required", name)
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	}
	env := os.Environ()
	if len(cfg.Env) > 0 {
		keys := make([]string, 0, len(cfg.Env))
		for k := range cfg.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			env = append(env, k+"="+expandHeaderEnv(cfg.Env[k]))
		}
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdio upstream %q stdin pipe: %w", name, err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdio upstream %q stdout pipe: %w", name, err)
	}
	stderrBuf := &cappedBuffer{buf: &bytes.Buffer{}, limit: 16 * 1024}
	u := &StdioUpstream{
		name:   name,
		cfg:    cfg,
		log:    log,
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReaderSize(stdoutPipe, 64*1024),
		stderr: stderrBuf,
	}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start stdio upstream %q (%s): %w", name, cfg.Command, err)
	}

	initCtx, cancel := context.WithTimeout(ctx, stdioInitTimeout)
	defer cancel()
	initParams := map[string]any{
		"protocolVersion": stdioProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "caveman-codemode",
			"version": "1.0.0",
		},
	}
	if _, err := u.roundTrip(initCtx, "initialize", initParams); err != nil {
		_ = u.Close()
		return nil, fmt.Errorf("initialize stdio upstream %q: %w", name, err)
	}
	if err := u.notify("notifications/initialized", map[string]any{}); err != nil {
		_ = u.Close()
		return nil, fmt.Errorf("notify initialized %q: %w", name, err)
	}
	return u, nil
}

// Name returns the registered MCP server name.
func (u *StdioUpstream) Name() string { return u.name }

// ListTools queries tools/list from the downstream MCP server (cached after
// first successful fetch so Gateway.AddUpstream does not re-spawn round-trips).
func (u *StdioUpstream) ListTools(ctx context.Context) ([]ToolSpec, error) {
	u.mu.Lock()
	if u.hasTools {
		out := make([]ToolSpec, len(u.tools))
		copy(out, u.tools)
		u.mu.Unlock()
		return out, nil
	}
	u.mu.Unlock()

	callCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, stdioInitTimeout)
		defer cancel()
	}

	var all []ToolSpec
	var cursor string
	for range 16 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := u.roundTrip(callCtx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools []struct {
				Name        string           `json:"name"`
				Description string           `json:"description"`
				InputSchema map[string]any   `json:"inputSchema"`
				Annotations *ToolAnnotations `json:"annotations,omitempty"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("decode tools/list from %q: %w", u.name, err)
		}
		for _, t := range page.Tools {
			if strings.TrimSpace(t.Name) == "" {
				continue
			}
			all = append(all, ToolSpec{
				Server:      u.name,
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
				Annotations: t.Annotations,
			})
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	u.mu.Lock()
	u.tools = all
	u.hasTools = true
	out := make([]ToolSpec, len(all))
	copy(out, all)
	u.mu.Unlock()
	return out, nil
}

// CallTool invokes tools/call on the downstream MCP server and normalizes the
// MCP content response into structured JSON (when the text payload is JSON) or
// plain text.
func (u *StdioUpstream) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	if args == nil {
		args = map[string]any{}
	}
	callCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, stdioCallTimeout)
		defer cancel()
	}
	raw, err := u.roundTrip(callCtx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent any  `json:"structuredContent,omitempty"`
		IsError           bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode tools/call result from %q.%s: %w", u.name, name, err)
	}
	var texts []string
	for _, c := range resp.Content {
		if c.Text != "" {
			texts = append(texts, c.Text)
		}
	}
	joined := strings.Join(texts, "\n")
	if resp.IsError {
		if joined == "" {
			joined = "downstream tool returned an error"
		}
		return nil, fmt.Errorf("%s.%s: %s", u.name, name, joined)
	}
	if resp.StructuredContent != nil {
		return resp.StructuredContent, nil
	}
	return decodeMCPTextPayload(joined), nil
}

// Close terminates the child MCP server process cleanly.
func (u *StdioUpstream) Close() error {
	if !u.closed.CompareAndSwap(false, true) {
		return nil
	}
	if u.stdin != nil {
		_ = u.stdin.Close()
	}
	if u.cmd != nil && u.cmd.Process != nil {
		done := make(chan error, 1)
		go func() { done <- u.cmd.Wait() }()
		select {
		case <-done:
			return nil
		case <-time.After(1500 * time.Millisecond):
			_ = u.cmd.Process.Kill()
			<-done
			return nil
		}
	}
	return nil
}

func (u *StdioUpstream) notify(method string, params any) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed.Load() {
		return errors.New("stdio upstream closed")
	}
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	frame = append(frame, '\n')
	_, err = u.stdin.Write(frame)
	return err
}

func (u *StdioUpstream) roundTrip(ctx context.Context, method string, params any) (json.RawMessage, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed.Load() {
		return nil, errors.New("stdio upstream closed")
	}
	id := u.nextID.Add(1)
	reqBytes, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, err
	}
	reqBytes = append(reqBytes, '\n')
	if _, err := u.stdin.Write(reqBytes); err != nil {
		return nil, fmt.Errorf("write %s: %w", method, err)
	}

	type readRes struct {
		result json.RawMessage
		err    error
	}
	ch := make(chan readRes, 1)
	go func() {
		for {
			line, rerr := readLineCapped(u.stdout, stdioMaxLineBytes)
			if rerr != nil {
				if detail := strings.TrimSpace(u.stderr.String()); detail != "" {
					ch <- readRes{err: fmt.Errorf("read %s: %w (stderr: %s)", method, rerr, firstSentence(detail, 240))}
				} else {
					ch <- readRes{err: fmt.Errorf("read %s: %w", method, rerr)}
				}
				return
			}
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(line, &msg); err != nil {
				// Ignore stray non-JSON stdout noise from misbehaving servers.
				continue
			}
			if len(msg.ID) == 0 || string(msg.ID) == "null" {
				// Server notification; keep reading for our response.
				continue
			}
			if !jsonIDMatches(msg.ID, id) {
				continue
			}
			if msg.Error != nil {
				ch <- readRes{err: fmt.Errorf("rpc error %d: %s", msg.Error.Code, msg.Error.Message)}
				return
			}
			ch <- readRes{result: msg.Result}
			return
		}
	}()

	select {
	case <-ctx.Done():
		// Terminate stuck child so the reader goroutine unblocks.
		_ = u.Close()
		return nil, fmt.Errorf("%s timed out: %w", method, ctx.Err())
	case res := <-ch:
		return res.result, res.err
	}
}

func jsonIDMatches(raw json.RawMessage, want int64) bool {
	s := strings.TrimSpace(string(raw))
	if s == strconv.FormatInt(want, 10) {
		return true
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1:len(s)-1] == strconv.FormatInt(want, 10)
	}
	return false
}

func readLineCapped(r *bufio.Reader, maxBytes int) ([]byte, error) {
	var out []byte
	exceeded := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !exceeded {
			if len(out)+len(chunk) > maxBytes {
				exceeded = true
				out = nil
			} else {
				out = append(out, chunk...)
			}
		}
		if err == nil {
			if exceeded {
				return nil, fmt.Errorf("response line exceeded %d bytes", maxBytes)
			}
			return out, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if exceeded {
			return nil, fmt.Errorf("response line exceeded %d bytes", maxBytes)
		}
		if errors.Is(err, io.EOF) && len(out) > 0 {
			return out, nil
		}
		return nil, err
	}
}

func decodeMCPTextPayload(text string) any {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	if (trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}') ||
		(trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']') {
		var parsed any
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&parsed); err == nil {
			var extra any
			if dec.Decode(&extra) == io.EOF {
				return parsed
			}
		}
	}
	return text
}

type cappedBuffer struct {
	mu    sync.Mutex
	buf   *bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	remain := c.limit - c.buf.Len()
	if remain > 0 {
		if len(p) > remain {
			_, _ = c.buf.Write(p[:remain])
		} else {
			_, _ = c.buf.Write(p)
		}
	}
	c.mu.Unlock()
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// --- MCP Config Loading & Discovery -----------------------------------------

// LoadConfigFile parses a JSON MCP config file supporting standard
// `{"mcpServers": {...}}` (Claude / Cursor / .mcp.json) or `{"servers": {...}}`
// formats. Remote HTTP-only entries without a `command` are skipped.
func LoadConfigFile(path string) (map[string]ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseMCPConfigJSON(data)
}

// ParseMCPConfigJSON parses a JSON MCP config payload (`mcpServers` or `servers`),
// retaining both stdio (`command`) and remote Streamable HTTP (`url`) servers.
func ParseMCPConfigJSON(data []byte) (map[string]ServerConfig, error) {
	var raw struct {
		MCPServers map[string]ServerConfig `json:"mcpServers"`
		Servers    map[string]ServerConfig `json:"servers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := map[string]ServerConfig{}
	for name, cfg := range raw.Servers {
		if !cfg.Valid() || isSelfServer(name, cfg) {
			continue
		}
		out[name] = cfg
	}
	for name, cfg := range raw.MCPServers {
		if !cfg.Valid() || isSelfServer(name, cfg) {
			continue
		}
		out[name] = cfg
	}
	return out, nil
}

// DiscoverUserMCPConfigs scans standard coding-agent MCP locations in precedence
// order (user-global first, project-local overrides user-global):
//   1. `<home>/.cursor/mcp.json`
//   2. `<home>/.openclaw/openclaw.json` (`mcp.servers`)
//   3. `<home>/.hermes/config.yaml` (`mcp_servers:`)
//   4. `<home>/.config/opencode/opencode.json` (`mcp`)
//   5. `<home>/.gemini/settings.json` (`mcpServers`)
//   6. `<home>/.codex/config.toml` (`[mcp_servers.<name>]`)
//   7. `<home>/.claude.json` (`mcpServers` + `projects[<workspace>].mcpServers`)
//   8. `<workspace>/.gemini/settings.json` (`mcpServers`)
//   9. `<workspace>/.mcp.json` (project-local highest precedence)
//
// Self-referential Caveman MCP entries are automatically excluded to prevent
// recursive spawning.
func DiscoverUserMCPConfigs(workspaceDir, homeDir string) map[string]ServerConfig {
	merged := map[string]ServerConfig{}
	merge := func(src map[string]ServerConfig) {
		for k, v := range src {
			if !v.Valid() || isSelfServer(k, v) {
				continue
			}
			merged[k] = v
		}
	}

	if homeDir != "" {
		// 1. Cursor (~/.cursor/mcp.json)
		if cfg, err := LoadConfigFile(filepath.Join(homeDir, ".cursor", "mcp.json")); err == nil {
			merge(cfg)
		}
		// 2. OpenClaw (~/.openclaw/openclaw.json)
		if data, err := os.ReadFile(filepath.Join(homeDir, ".openclaw", "openclaw.json")); err == nil {
			merge(parseOpenClawJSON(data))
		}
		// 3. Hermes ($HERMES_HOME/config.yaml, else ~/.hermes/config.yaml)
		hermesDir := strings.TrimSpace(os.Getenv("HERMES_HOME"))
		if hermesDir == "" {
			hermesDir = filepath.Join(homeDir, ".hermes")
		}
		if data, err := os.ReadFile(filepath.Join(hermesDir, "config.yaml")); err == nil {
			merge(ParseHermesMCPConfig(data))
		}
		// 4. OpenCode (~/.config/opencode/opencode.json)
		if data, err := os.ReadFile(filepath.Join(homeDir, ".config", "opencode", "opencode.json")); err == nil {
			merge(parseOpenCodeJSON(data))
		}
		// 5. Gemini CLI (~/.gemini/settings.json)
		if cfg, err := LoadConfigFile(filepath.Join(homeDir, ".gemini", "settings.json")); err == nil {
			merge(cfg)
		}
		// 5b. Qwen Code (~/.qwen/settings.json)
		if cfg, err := LoadConfigFile(filepath.Join(homeDir, ".qwen", "settings.json")); err == nil {
			merge(cfg)
		}
		// 6. Codex ($CODEX_HOME/config.toml, else ~/.codex/config.toml)
		codexDir := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if codexDir == "" {
			codexDir = filepath.Join(homeDir, ".codex")
		}
		if data, err := os.ReadFile(filepath.Join(codexDir, "config.toml")); err == nil {
			merge(ParseCodexMCPConfig(data))
		}
		// 7. Claude ($CLAUDE_CONFIG_DIR/.claude.json, else ~/.claude.json)
		claudeDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
		claudePath := filepath.Join(homeDir, ".claude.json")
		if claudeDir != "" {
			claudePath = filepath.Join(claudeDir, ".claude.json")
		}
		if data, err := os.ReadFile(claudePath); err == nil {
			merge(parseClaudeUserJSON(data, workspaceDir))
		}
	}
	if workspaceDir != "" {
		// 8. Project Gemini (.gemini/settings.json) & Qwen (.qwen/settings.json)
		if cfg, err := LoadConfigFile(filepath.Join(workspaceDir, ".gemini", "settings.json")); err == nil {
			merge(cfg)
		}
		if cfg, err := LoadConfigFile(filepath.Join(workspaceDir, ".qwen", "settings.json")); err == nil {
			merge(cfg)
		}
		// 9. Project .mcp.json
		mcpPath := filepath.Join(workspaceDir, ".mcp.json")
		if cfg, err := LoadConfigFile(mcpPath); err == nil {
			merge(cfg)
		}
	}
	return merged
}

func parseOpenClawJSON(data []byte) map[string]ServerConfig {
	var doc struct {
		MCP struct {
			Servers map[string]ServerConfig `json:"servers"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	out := map[string]ServerConfig{}
	for name, cfg := range doc.MCP.Servers {
		if cfg.Valid() && !isSelfServer(name, cfg) {
			out[name] = cfg
		}
	}
	return out
}

func parseOpenCodeJSON(data []byte) map[string]ServerConfig {
	var doc struct {
		MCP map[string]struct {
			Type        string            `json:"type"`
			Command     []string          `json:"command"`
			Environment map[string]string `json:"environment"`
			URL         string            `json:"url"`
			Headers     map[string]string `json:"headers"`
			Enabled     *bool             `json:"enabled"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	out := map[string]ServerConfig{}
	for name, entry := range doc.MCP {
		if entry.Enabled != nil && !*entry.Enabled {
			continue
		}
		var cfg ServerConfig
		if entry.URL != "" {
			cfg.URL = entry.URL
			cfg.Headers = entry.Headers
		} else if len(entry.Command) > 0 {
			cfg.Command = entry.Command[0]
			if len(entry.Command) > 1 {
				cfg.Args = entry.Command[1:]
			}
			cfg.Env = entry.Environment
		}
		if cfg.Valid() && !isSelfServer(name, cfg) {
			out[name] = cfg
		}
	}
	return out
}

// ParseHermesMCPConfig extracts `mcp_servers:` entries from Hermes
// `config.yaml` without external YAML dependencies, honoring `enabled: false`,
// `tools.include` / `tools.exclude`, dynamic 1/2/4-space indentation, unquoted
// flow lists, inline flow maps, and YAML 1.2 comment rules (preserving `#`
// inside URL fragments).
func ParseHermesMCPConfig(data []byte) map[string]ServerConfig {
	out := map[string]ServerConfig{}
	disabled := map[string]bool{}
	inSection := false
	curServer := ""
	serverIndent := -1
	sectionMode := "" // "", "args", "env", "headers", "tools", "tools_include", "tools_exclude"
	sectionIndent := -1
	toolsIndent := -1

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		rawLine := sc.Text()
		trimmed := strings.TrimSpace(stripYAMLComment(rawLine))
		if trimmed == "" || trimmed == "---" || trimmed == "..." {
			continue
		}
		indent := len(rawLine) - len(strings.TrimLeft(rawLine, " "))
		if indent == 0 {
			inSection = trimmed == "mcp_servers:" || trimmed == `"mcp_servers":` || trimmed == `'mcp_servers':`
			curServer = ""
			serverIndent = -1
			sectionMode = ""
			sectionIndent = -1
			toolsIndent = -1
			continue
		}
		if !inSection {
			continue
		}
		if serverIndent < 0 || indent == serverIndent {
			if strings.HasSuffix(trimmed, ":") && !strings.HasPrefix(trimmed, "-") {
				curServer = unquoteTOMLKey(strings.TrimSuffix(trimmed, ":"))
				serverIndent = indent
				sectionMode = ""
				sectionIndent = -1
				toolsIndent = -1
				continue
			}
		}
		if curServer == "" || indent <= serverIndent {
			continue
		}
		cfg := out[curServer]

		// Close sub-blocks whenever indentation returns to or left of their header.
		if sectionMode == "tools_include" || sectionMode == "tools_exclude" {
			if indent < sectionIndent || (indent == sectionIndent && !strings.HasPrefix(trimmed, "-")) {
				sectionMode = "tools"
				sectionIndent = toolsIndent
			}
		}
		if sectionMode == "tools" && indent <= toolsIndent {
			sectionMode = ""
			sectionIndent = -1
			toolsIndent = -1
		}
		if (sectionMode == "env" || sectionMode == "headers") && indent <= sectionIndent {
			sectionMode = ""
			sectionIndent = -1
		}
		if sectionMode == "args" && (indent < sectionIndent || (indent == sectionIndent && !strings.HasPrefix(trimmed, "-"))) {
			sectionMode = ""
			sectionIndent = -1
		}

		if strings.HasPrefix(trimmed, "- ") {
			item := unquoteTOMLKey(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			switch sectionMode {
			case "args":
				cfg.Args = append(cfg.Args, item)
			case "tools_include":
				cfg.IncludeTools = append(cfg.IncludeTools, item)
			case "tools_exclude":
				cfg.ExcludeTools = append(cfg.ExcludeTools, item)
			}
			out[curServer] = cfg
			continue
		}

		colon := strings.IndexByte(trimmed, ':')
		if colon <= 0 {
			continue
		}
		key := unquoteTOMLKey(strings.TrimSpace(trimmed[:colon]))
		val := strings.TrimSpace(trimmed[colon+1:])

		if sectionMode == "env" && indent > sectionIndent {
			if cfg.Env == nil {
				cfg.Env = map[string]string{}
			}
			cfg.Env[key] = unquoteTOMLKey(val)
			out[curServer] = cfg
			continue
		}
		if sectionMode == "headers" && indent > sectionIndent {
			if cfg.Headers == nil {
				cfg.Headers = map[string]string{}
			}
			cfg.Headers[key] = unquoteTOMLKey(val)
			out[curServer] = cfg
			continue
		}
		if sectionMode == "tools" && indent > toolsIndent {
			switch key {
			case "include":
				if val == "" {
					sectionMode = "tools_include"
					sectionIndent = indent
				} else if arr, ok := parseYAMLFlowList(val); ok {
					cfg.IncludeTools = append(cfg.IncludeTools, arr...)
				}
			case "exclude":
				if val == "" {
					sectionMode = "tools_exclude"
					sectionIndent = indent
				} else if arr, ok := parseYAMLFlowList(val); ok {
					cfg.ExcludeTools = append(cfg.ExcludeTools, arr...)
				}
			}
			out[curServer] = cfg
			continue
		}

		sectionMode = ""
		sectionIndent = -1
		switch key {
		case "enabled":
			if strings.EqualFold(unquoteTOMLKey(val), "false") {
				disabled[curServer] = true
			}
		case "command":
			cfg.Command = unquoteTOMLKey(val)
		case "url":
			cfg.URL = unquoteTOMLKey(val)
		case "cwd":
			cfg.Cwd = unquoteTOMLKey(val)
		case "args":
			if val == "" {
				sectionMode = "args"
				sectionIndent = indent
			} else if arr, ok := parseYAMLFlowList(val); ok {
				cfg.Args = arr
			}
		case "env":
			if val == "" {
				sectionMode = "env"
				sectionIndent = indent
			} else if m, ok := parseYAMLFlowMap(val); ok {
				if cfg.Env == nil {
					cfg.Env = map[string]string{}
				}
				for k, v := range m {
					cfg.Env[k] = v
				}
			}
		case "headers":
			if val == "" {
				sectionMode = "headers"
				sectionIndent = indent
			} else if m, ok := parseYAMLFlowMap(val); ok {
				if cfg.Headers == nil {
					cfg.Headers = map[string]string{}
				}
				for k, v := range m {
					cfg.Headers[k] = v
				}
			}
		case "tools":
			if val == "" {
				sectionMode = "tools"
				sectionIndent = indent
				toolsIndent = indent
			}
		}
		out[curServer] = cfg
	}
	for name, cfg := range out {
		if disabled[name] || !cfg.Valid() || isSelfServer(name, cfg) {
			delete(out, name)
		}
	}
	return out
}

func stripYAMLComment(line string) string {
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if inDouble && c == '\\' {
			escaped = true
			continue
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		// YAML 1.2: `#` starts a comment ONLY at col 0 or after whitespace.
		if c == '#' && !inSingle && !inDouble && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
}

func parseYAMLFlowList(raw string) ([]string, bool) {
	s := strings.TrimSpace(raw)
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, false
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return []string{}, true
	}
	parts := splitTOMLList(inner)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := unquoteTOMLKey(strings.TrimSpace(p))
		if v != "" {
			out = append(out, v)
		}
	}
	return out, true
}

func parseYAMLFlowMap(raw string) (map[string]string, bool) {
	s := strings.TrimSpace(raw)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, false
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return map[string]string{}, true
	}
	out := map[string]string{}
	for _, part := range splitTOMLList(inner) {
		colon := strings.IndexByte(part, ':')
		if colon <= 0 {
			continue
		}
		k := unquoteTOMLKey(strings.TrimSpace(part[:colon]))
		v := unquoteTOMLKey(strings.TrimSpace(part[colon+1:]))
		if k != "" {
			out[k] = v
		}
	}
	return out, true
}

func parseClaudeUserJSON(data []byte, workspaceDir string) map[string]ServerConfig {
	var doc struct {
		MCPServers map[string]ServerConfig `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers map[string]ServerConfig `json:"mcpServers"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	out := map[string]ServerConfig{}
	for name, cfg := range doc.MCPServers {
		if cfg.Valid() && !isSelfServer(name, cfg) {
			out[name] = cfg
		}
	}
	if workspaceDir != "" {
		cleanWS := filepath.Clean(workspaceDir)
		for projPath, proj := range doc.Projects {
			if filepath.Clean(projPath) != cleanWS {
				continue
			}
			for name, cfg := range proj.MCPServers {
				if cfg.Valid() && !isSelfServer(name, cfg) {
					out[name] = cfg
				}
			}
		}
	}
	return out
}

// ParseCodexMCPConfig extracts `[mcp_servers.<name>]`, `.env`, `.http_headers`,
// and `.env_http_headers` sections from a Codex `config.toml` file, honoring
// quoted dotted server names, multiline `args`, `enabled = false`,
// `bearer_token_env_var`, `env_http_headers`, and `env_vars`.
func ParseCodexMCPConfig(data []byte) map[string]ServerConfig {
	out := map[string]ServerConfig{}
	disabled := map[string]bool{}
	var curName string
	subTable := "" // "", "env", "http_headers", "env_http_headers"
	collectingArgs := false
	var argsBuf strings.Builder

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(stripTOMLComment(sc.Text()))
		if line == "" {
			continue
		}
		if collectingArgs {
			argsBuf.WriteByte(' ')
			argsBuf.WriteString(line)
			if strings.Contains(line, "]") {
				if arr, ok := parseTOMLStringArray(argsBuf.String()); ok && curName != "" {
					cfg := out[curName]
					cfg.Args = arr
					out[curName] = cfg
				}
				collectingArgs = false
				argsBuf.Reset()
			}
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			header := strings.TrimSpace(line[1 : len(line)-1])
			curName = ""
			subTable = ""
			if rest, ok := strings.CutPrefix(header, "mcp_servers."); ok {
				name, sub := splitCodexMCPHeader(rest)
				curName = name
				subTable = sub
			}
			continue
		}
		if curName == "" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := unquoteTOMLKey(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:])
		cfg := out[curName]
		switch subTable {
		case "env":
			if cfg.Env == nil {
				cfg.Env = map[string]string{}
			}
			if s, ok := parseTOMLString(val); ok {
				cfg.Env[key] = s
			}
			out[curName] = cfg
			continue
		case "http_headers", "headers":
			if cfg.Headers == nil {
				cfg.Headers = map[string]string{}
			}
			if s, ok := parseTOMLString(val); ok {
				cfg.Headers[key] = s
			}
			out[curName] = cfg
			continue
		case "env_http_headers":
			if cfg.Headers == nil {
				cfg.Headers = map[string]string{}
			}
			if envName, ok := parseTOMLString(val); ok && envName != "" {
				cfg.Headers[key] = "${" + envName + "}"
			}
			out[curName] = cfg
			continue
		}
		switch key {
		case "enabled":
			if strings.EqualFold(val, "false") {
				disabled[curName] = true
			}
		case "command":
			if s, ok := parseTOMLString(val); ok {
				cfg.Command = s
			}
		case "url":
			if s, ok := parseTOMLString(val); ok {
				cfg.URL = s
			}
		case "cwd":
			if s, ok := parseTOMLString(val); ok {
				cfg.Cwd = s
			}
		case "args":
			if strings.HasPrefix(val, "[") && !strings.HasSuffix(val, "]") {
				collectingArgs = true
				argsBuf.Reset()
				argsBuf.WriteString(val)
			} else if arr, ok := parseTOMLStringArray(val); ok {
				cfg.Args = arr
			}
		case "bearer_token_env_var":
			if envName, ok := parseTOMLString(val); ok && envName != "" {
				if cfg.Headers == nil {
					cfg.Headers = map[string]string{}
				}
				cfg.Headers["Authorization"] = "Bearer ${" + envName + "}"
			}
		case "env_http_headers":
			if m, ok := parseTOMLInlineTable(val); ok {
				if cfg.Headers == nil {
					cfg.Headers = map[string]string{}
				}
				for hk, envName := range m {
					if envName != "" {
						cfg.Headers[hk] = "${" + envName + "}"
					}
				}
			}
		case "http_headers", "headers":
			if m, ok := parseTOMLInlineTable(val); ok {
				if cfg.Headers == nil {
					cfg.Headers = map[string]string{}
				}
				for k, v := range m {
					cfg.Headers[k] = v
				}
			}
		case "env":
			if m, ok := parseTOMLInlineTable(val); ok {
				if cfg.Env == nil {
					cfg.Env = map[string]string{}
				}
				for k, v := range m {
					cfg.Env[k] = v
				}
			}
		case "env_vars":
			if arr, ok := parseTOMLStringArray(val); ok {
				if cfg.Env == nil {
					cfg.Env = map[string]string{}
				}
				for _, envName := range arr {
					if envName != "" {
						cfg.Env[envName] = "${" + envName + "}"
					}
				}
			}
		}
		out[curName] = cfg
	}
	for name, cfg := range out {
		if disabled[name] || !cfg.Valid() || isSelfServer(name, cfg) {
			delete(out, name)
		}
	}
	return out
}

func splitCodexMCPHeader(rest string) (serverName, subTable string) {
	rest = strings.TrimSpace(rest)
	for _, suffix := range []string{".env", ".http_headers", ".headers", ".env_http_headers"} {
		if strings.HasSuffix(rest, suffix) {
			return unquoteTOMLKey(strings.TrimSuffix(rest, suffix)), strings.TrimPrefix(suffix, ".")
		}
	}
	// Quoted dotted server name: `"foo.bar"` or `'foo.bar'`
	if (strings.HasPrefix(rest, `"`) && strings.HasSuffix(rest, `"`)) ||
		(strings.HasPrefix(rest, `'`) && strings.HasSuffix(rest, `'`)) {
		return unquoteTOMLKey(rest), ""
	}
	if strings.Contains(rest, ".") {
		return "", ""
	}
	return unquoteTOMLKey(rest), ""
}

func isSelfServer(name string, cfg ServerConfig) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "caveman" || lower == "caveman-codemode" || lower == "caveman-mcp" {
		return true
	}
	base := strings.ToLower(filepath.Base(strings.TrimSpace(cfg.Command)))
	if base == "caveman-mcp" || base == "caveman-mcp.exe" {
		return true
	}
	if (base == "caveman" || base == "caveman.exe") && len(cfg.Args) > 0 && strings.EqualFold(cfg.Args[0], "codemode") {
		return true
	}
	return false
}

func stripTOMLComment(line string) string {
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if inDouble && c == '\\' {
			escaped = true
			continue
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if c == '#' && !inSingle && !inDouble {
			return line[:i]
		}
	}
	return line
}

func unquoteTOMLKey(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

func parseTOMLString(val string) (string, bool) {
	val = strings.TrimSpace(val)
	if len(val) < 2 {
		return "", false
	}
	if val[0] == '"' && val[len(val)-1] == '"' {
		var s string
		if err := json.Unmarshal([]byte(val), &s); err == nil {
			return s, true
		}
	}
	if val[0] == '\'' && val[len(val)-1] == '\'' {
		return val[1 : len(val)-1], true
	}
	return "", false
}

func parseTOMLStringArray(val string) ([]string, bool) {
	val = strings.TrimSpace(val)
	if len(val) < 2 || val[0] != '[' || val[len(val)-1] != ']' {
		return nil, false
	}
	var out []string
	if err := json.Unmarshal([]byte(val), &out); err == nil {
		return out, true
	}
	// Fallback for single-quoted TOML string arrays.
	inner := strings.TrimSpace(val[1 : len(val)-1])
	if inner == "" {
		return []string{}, true
	}
	parts := splitTOMLList(inner)
	out = make([]string, 0, len(parts))
	for _, p := range parts {
		s, ok := parseTOMLString(p)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func parseTOMLInlineTable(val string) (map[string]string, bool) {
	val = strings.TrimSpace(val)
	if len(val) < 2 || val[0] != '{' || val[len(val)-1] != '}' {
		return nil, false
	}
	inner := strings.TrimSpace(val[1 : len(val)-1])
	if inner == "" {
		return map[string]string{}, true
	}
	out := map[string]string{}
	for _, item := range splitTOMLList(inner) {
		eq := strings.IndexByte(item, '=')
		if eq <= 0 {
			continue
		}
		k := unquoteTOMLKey(strings.TrimSpace(item[:eq]))
		if v, ok := parseTOMLString(strings.TrimSpace(item[eq+1:])); ok {
			out[k] = v
		}
	}
	return out, true
}

func splitTOMLList(s string) []string {
	var parts []string
	start := 0
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if inDouble && c == '\\' {
			escaped = true
			continue
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
		} else if c == '"' && !inSingle {
			inDouble = !inDouble
		} else if c == ',' && !inSingle && !inDouble {
			if p := strings.TrimSpace(s[start:i]); p != "" {
				parts = append(parts, p)
			}
			start = i + 1
		}
	}
	if p := strings.TrimSpace(s[start:]); p != "" {
		parts = append(parts, p)
	}
	return parts
}
