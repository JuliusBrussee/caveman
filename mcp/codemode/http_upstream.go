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
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	httpInitTimeout      = 15 * time.Second
	httpCallTimeout      = 45 * time.Second
	httpMaxBodySize      = 32 << 20 // 32 MiB
	httpProtocolVersion  = "2025-03-26"
)

var envExpandRE = regexp.MustCompile(`\$\{(?:env:)?([A-Za-z_][A-Za-z0-9_]*)\}`)

// HTTPUpstream wraps a remote MCP server speaking MCP Streamable HTTP
// (JSON-RPC 2.0 over HTTP POST returning application/json or text/event-stream
// SSE frames, with optional Mcp-Session-Id session tracking and header auth).
type HTTPUpstream struct {
	name    string
	url     string
	headers map[string]string
	client  *http.Client
	log     *slog.Logger

	mu              sync.Mutex
	sessionID       string
	protocolVersion string
	nextID          atomic.Int64
	tools           []ToolSpec
	hasTools        bool
}

// StartHTTPUpstream initializes a remote MCP Streamable HTTP server using the
// provided http.Client (defaults to an HTTP client with sane timeouts when nil).
func StartHTTPUpstream(ctx context.Context, name string, cfg ServerConfig, client *http.Client, log *slog.Logger) (*HTTPUpstream, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("http upstream name is required")
	}
	endpoint := strings.TrimSpace(cfg.URL)
	if endpoint == "" {
		return nil, fmt.Errorf("http upstream %q: url is required", name)
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, fmt.Errorf("http upstream %q: unsupported url scheme %q", name, endpoint)
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	hdrs := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		hdrs[k] = expandHeaderEnv(v)
	}
	httpClient := &http.Client{Timeout: httpCallTimeout}
	if client != nil {
		cp := *client
		httpClient = &cp
	}
	prevCheckRedirect := httpClient.CheckRedirect
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && !sameHTTPOrigin(via[0].URL, req.URL) {
			for k := range hdrs {
				req.Header.Del(k)
			}
			req.Header.Del("Authorization")
			req.Header.Del("Proxy-Authorization")
			req.Header.Del("Cookie")
			req.Header.Del("Mcp-Session-Id")
		}
		if prevCheckRedirect != nil {
			return prevCheckRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	u := &HTTPUpstream{
		name:    name,
		url:     endpoint,
		headers: hdrs,
		client:  httpClient,
		log:     log,
	}

	initCtx, cancel := context.WithTimeout(ctx, httpInitTimeout)
	defer cancel()
	initParams := map[string]any{
		"protocolVersion": httpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "caveman-codemode",
			"version": "1.0.0",
		},
	}
	initRaw, err := u.roundTrip(initCtx, "initialize", initParams)
	if err != nil {
		return nil, fmt.Errorf("initialize http upstream %q: %w", name, err)
	}
	var initResp struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(initRaw, &initResp) == nil && strings.TrimSpace(initResp.ProtocolVersion) != "" {
		u.mu.Lock()
		u.protocolVersion = strings.TrimSpace(initResp.ProtocolVersion)
		u.mu.Unlock()
	}
	// Best-effort notifications/initialized (some stateless remote MCP endpoints return 202/204/405).
	_ = u.notify(initCtx, "notifications/initialized", map[string]any{})
	return u, nil
}

// StartUpstream launches either an HTTPUpstream (when cfg.URL is set) or a
// StdioUpstream (when cfg.Command is set), enforcing any configured
// IncludeTools / ExcludeTools allowlist/denylist (e.g. Hermes `mcp_servers.*.tools`).
func StartUpstream(ctx context.Context, name string, cfg ServerConfig, log *slog.Logger) (Upstream, error) {
	var up Upstream
	var err error
	if strings.TrimSpace(cfg.URL) != "" {
		up, err = StartHTTPUpstream(ctx, name, cfg, nil, log)
	} else {
		up, err = StartStdioUpstream(ctx, name, cfg, log)
	}
	if err != nil {
		return nil, err
	}
	if len(cfg.IncludeTools) > 0 || len(cfg.ExcludeTools) > 0 {
		return newFilteredUpstream(up, cfg.IncludeTools, cfg.ExcludeTools), nil
	}
	return up, nil
}

type filteredUpstream struct {
	inner   Upstream
	include map[string]bool
	exclude map[string]bool
}

func newFilteredUpstream(inner Upstream, include, exclude []string) *filteredUpstream {
	f := &filteredUpstream{inner: inner}
	if len(include) > 0 {
		f.include = make(map[string]bool, len(include))
		for _, n := range include {
			f.include[n] = true
		}
	}
	if len(exclude) > 0 {
		f.exclude = make(map[string]bool, len(exclude))
		for _, n := range exclude {
			f.exclude[n] = true
		}
	}
	return f
}

func (f *filteredUpstream) allowed(toolName string) bool {
	if len(f.exclude) > 0 && f.exclude[toolName] {
		return false
	}
	if len(f.include) > 0 && !f.include[toolName] {
		return false
	}
	return true
}

func (f *filteredUpstream) Name() string { return f.inner.Name() }

func (f *filteredUpstream) ListTools(ctx context.Context) ([]ToolSpec, error) {
	specs, err := f.inner.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ToolSpec, 0, len(specs))
	for _, s := range specs {
		if f.allowed(s.Name) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *filteredUpstream) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	if !f.allowed(name) {
		return nil, fmt.Errorf("tool %q on server %q is blocked by server tool filter", name, f.inner.Name())
	}
	return f.inner.CallTool(ctx, name, args)
}

func (f *filteredUpstream) Close() error {
	if c, ok := f.inner.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (u *HTTPUpstream) Name() string { return u.name }

func (u *HTTPUpstream) ListTools(ctx context.Context) ([]ToolSpec, error) {
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
		callCtx, cancel = context.WithTimeout(ctx, httpInitTimeout)
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

func (u *HTTPUpstream) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	if args == nil {
		args = map[string]any{}
	}
	callCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, httpCallTimeout)
		defer cancel()
	}
	raw, err := u.roundTrip(callCtx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, err
	}
	return decodeMCPCallResult(u.name, name, raw)
}

func (u *HTTPUpstream) Close() error {
	u.mu.Lock()
	sid := u.sessionID
	u.sessionID = ""
	u.mu.Unlock()
	if sid == "" || u.client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.url, nil)
	if err != nil {
		return nil
	}
	u.applyHeaders(req)
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := u.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func (u *HTTPUpstream) notify(ctx context.Context, method string, params any) error {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	u.applyHeaders(req)
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func (u *HTTPUpstream) roundTrip(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := u.nextID.Add(1)
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	u.applyHeaders(req)

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http %s %s: %w", method, u.name, err)
	}
	defer resp.Body.Close()

	if sid := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); sid != "" {
		u.mu.Lock()
		u.sessionID = sid
		u.mu.Unlock()
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("http %d from %q: %s", resp.StatusCode, u.name, firstSentence(string(errBody), 240))
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/event-stream") {
		// Stream SSE frames incrementally and return as soon as the JSON-RPC
		// response matching `id` arrives, so servers that keep SSE streams open
		// for notifications do not block until EOF/timeout.
		return readSSERPCResponse(resp.Body, id, httpMaxBodySize)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, httpMaxBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s response from %q: %w", method, u.name, err)
	}
	if len(body) > httpMaxBodySize {
		return nil, fmt.Errorf("http response from %q exceeded %d bytes", u.name, httpMaxBodySize)
	}
	return parseJSONRPCResponse(body, id)
}

func (u *HTTPUpstream) applyHeaders(req *http.Request) {
	for k, v := range u.headers {
		if strings.TrimSpace(k) != "" && v != "" {
			req.Header.Set(k, v)
		}
	}
	// Protocol-required headers are applied last so custom config headers cannot clobber them.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	u.mu.Lock()
	sid := u.sessionID
	pv := u.protocolVersion
	u.mu.Unlock()
	if pv != "" {
		req.Header.Set("MCP-Protocol-Version", pv)
	}
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
}

func parseJSONRPCResponse(data []byte, wantID int64) (json.RawMessage, error) {
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &msg); err != nil {
		return nil, fmt.Errorf("decode rpc response: %w", err)
	}
	if msg.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", msg.Error.Code, msg.Error.Message)
	}
	if len(msg.ID) > 0 && string(msg.ID) != "null" && !jsonIDMatches(msg.ID, wantID) {
		return nil, fmt.Errorf("rpc response id mismatch: got %s want %d", string(msg.ID), wantID)
	}
	return msg.Result, nil
}

func parseSSERPCResponse(data []byte, wantID int64) (json.RawMessage, error) {
	return readSSERPCResponse(bytes.NewReader(data), wantID, httpMaxBodySize)
}

func readSSERPCResponse(r io.Reader, wantID int64, maxBytes int) (json.RawMessage, error) {
	lr := &io.LimitedReader{R: r, N: int64(maxBytes) + 1}
	sc := bufio.NewScanner(lr)
	sc.Buffer(make([]byte, 0, 64*1024), maxBytes)
	var eventLines []string
	flush := func() (json.RawMessage, bool, error) {
		if len(eventLines) == 0 {
			return nil, false, nil
		}
		joined := strings.TrimSpace(strings.Join(eventLines, "\n"))
		eventLines = eventLines[:0]
		if joined == "" || joined == "[DONE]" {
			return nil, false, nil
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(joined), &msg); err != nil {
			return nil, false, nil
		}
		if len(msg.ID) == 0 || string(msg.ID) == "null" || !jsonIDMatches(msg.ID, wantID) {
			return nil, false, nil
		}
		if msg.Error != nil {
			return nil, true, fmt.Errorf("rpc error %d: %s", msg.Error.Code, msg.Error.Message)
		}
		return msg.Result, true, nil
	}
	for sc.Scan() {
		if lr.N <= 0 {
			return nil, fmt.Errorf("sse response exceeded %d bytes", maxBytes)
		}
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			if res, matched, err := flush(); matched {
				return res, err
			}
			continue
		}
		if payload, ok := strings.CutPrefix(line, "data:"); ok {
			eventLines = append(eventLines, strings.TrimSpace(payload))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read sse stream: %w", err)
	}
	if lr.N <= 0 {
		return nil, fmt.Errorf("sse response exceeded %d bytes", maxBytes)
	}
	if res, matched, err := flush(); matched {
		return res, err
	}
	return nil, errors.New("no matching JSON-RPC response found in SSE stream")
}

func sameHTTPOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	if !strings.EqualFold(a.Scheme, b.Scheme) {
		return false
	}
	if !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	return effectiveURLPort(a) == effectiveURLPort(b)
}

func effectiveURLPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

func decodeMCPCallResult(server, name string, raw json.RawMessage) (any, error) {
	var resp struct {
		Content           []json.RawMessage `json:"content"`
		StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
		IsError           bool              `json:"isError"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode tools/call result from %q.%s: %w", server, name, err)
	}
	var texts []string
	allTextOnly := true
	for _, rawBlock := range resp.Content {
		var blk struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(rawBlock, &blk) != nil {
			allTextOnly = false
			continue
		}
		if blk.Type != "" && blk.Type != "text" {
			allTextOnly = false
		}
		if blk.Text != "" {
			texts = append(texts, blk.Text)
		}
	}
	joined := strings.Join(texts, "\n")
	if resp.IsError {
		if joined == "" {
			joined = "downstream tool returned an error"
		}
		return nil, fmt.Errorf("%s.%s: %s", server, name, joined)
	}
	trimmedStructured := bytes.TrimSpace(resp.StructuredContent)
	if len(trimmedStructured) > 0 && !bytes.Equal(trimmedStructured, []byte("null")) {
		var structured any
		dec := json.NewDecoder(bytes.NewReader(trimmedStructured))
		dec.UseNumber()
		if err := dec.Decode(&structured); err != nil {
			return nil, fmt.Errorf("decode structuredContent from %q.%s: %w", server, name, err)
		}
		return structured, nil
	}
	if !allTextOnly && len(resp.Content) > 0 {
		blocks := make([]any, 0, len(resp.Content))
		for _, rawBlock := range resp.Content {
			var decoded any
			dec := json.NewDecoder(bytes.NewReader(rawBlock))
			dec.UseNumber()
			if err := dec.Decode(&decoded); err == nil {
				blocks = append(blocks, decoded)
			}
		}
		out := map[string]any{"content": blocks}
		if joined != "" {
			out["text"] = joined
		}
		return out, nil
	}
	return decodeMCPTextPayload(joined), nil
}

func expandHeaderEnv(val string) string {
	return envExpandRE.ReplaceAllStringFunc(val, func(match string) string {
		inner := match[2 : len(match)-1]
		inner = strings.TrimPrefix(inner, "env:")
		return os.Getenv(inner)
	})
}
