// Package mcp is a reusable stdio JSON-RPC adapter for MCP tool servers. It owns
// the MCP framing — initialize, tools/list, tools/call, notifications — and
// dispatches to a caller-supplied set of Tools. The Caveman compression tools
// are one such set (see EngineTools); cavemem registers its own. Logs go to the
// injected logger only — stdout carries the protocol exclusively. Servers built
// here open no network connection.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

// Handler runs one tool call. It receives the raw JSON `arguments` and returns a
// ToolResult and must never write to stdout. A handler SHOULD not panic, but if
// one does the server contains it (recover → cave_tool_panicked) rather than
// crashing the process — the stdio server is un-killable by anything short of EOF.
type Handler func(args json.RawMessage) ToolResult

// Inbound-line and per-result size caps. A single JSON-RPC message longer than
// maxInboundBytes is rejected (cave_payload_too_large) rather than buffered
// unbounded; a tool result whose content exceeds maxResultBytes is replaced with
// the same fail-closed error rather than dumped whole into the host context.
// Both are generous — they guard pathological inputs, not normal payloads.
const (
	defaultMaxInboundBytes = 16 << 20 // 16 MiB
	defaultMaxResultBytes  = 16 << 20 // 16 MiB
)

// Tool is one MCP tool: its advertised name/description/schema plus its handler.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Meta        map[string]any
	Handler     Handler
	// ExemptResultCap declares that this tool returns the exact original bytes,
	// which must NEVER be truncated or rejected by maxResultBytes — recovery
	// paths (caveman_retrieve) set it, because the CCR store is shared with a
	// gateway that has no matching ceiling, so an original can legitimately
	// exceed the cap and failing it closed would make elided content
	// unrecoverable (root CLAUDE.md rule #2: recovery returns the original unchanged).
	ExemptResultCap bool
}

const defaultProtocolVersion = "2024-11-05"

// supportedProtocolVersions is deliberately explicit. The adapter implements
// the 2024-11-05 stdio framing/capability contract; echoing a newer client
// version would let the client assume semantics this process does not provide.
var supportedProtocolVersions = map[string]struct{}{
	defaultProtocolVersion: {},
}

// Server serves a fixed set of tools over a JSON-RPC stream.
type Server struct {
	tools           []Tool
	index           map[string]Handler
	capExempt       map[string]bool
	log             *slog.Logger
	name            string
	version         string
	maxInboundBytes int
	maxResultBytes  int
}

// NewServer builds a server over the given ordered tool set. log must write to
// stderr (never stdout); if nil, logging is discarded. The server name is
// reported in initialize.
func NewServer(name string, tools []Tool, log *slog.Logger) *Server {
	return NewServerVersion(name, "1.0.0", tools, log)
}

// NewServerVersion builds a server whose MCP initialize response carries the
// caller's build-stamped version. Command binaries use this constructor so the
// compatibility signal cannot drift from `version --json`.
func NewServerVersion(name, version string, tools []Tool, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if version == "" {
		version = "dev"
	}
	idx := make(map[string]Handler, len(tools))
	exempt := make(map[string]bool)
	for _, t := range tools {
		idx[t.Name] = t.Handler
		if t.ExemptResultCap {
			exempt[t.Name] = true
		}
	}
	return &Server{
		tools:           tools,
		index:           idx,
		capExempt:       exempt,
		log:             log,
		name:            name,
		version:         version,
		maxInboundBytes: defaultMaxInboundBytes,
		maxResultBytes:  defaultMaxResultBytes,
	}
}

// Serve runs the JSON-RPC loop until in reaches EOF. Framing is line-delimited
// (the MCP stdio contract): one JSON message per line, each response one compact
// JSON value terminated by a newline with no embedded newlines. The loop is
// resilient by construction — a malformed line, an over-long line, a batch, or a
// panicking handler is answered and then SKIPPED; nothing short of EOF ends the
// session (issue #139).
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	defer w.Flush()

	for {
		line, tooLong, err := readLine(r, s.maxInboundBytes)
		if tooLong {
			s.log.Warn("inbound message exceeds cap", "cap", s.maxInboundBytes)
			if werr := s.writeFlush(w, errorResponse(nil, codeInvalidRequest,
				fmt.Sprintf("cave_payload_too_large: message exceeds %d bytes", s.maxInboundBytes))); werr != nil {
				return werr
			}
		} else if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			if werr := s.handleLine(w, trimmed); werr != nil {
				return werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// readLine reads one newline-delimited message, bounded by max bytes. If the
// line exceeds max it is drained to the next newline (bounded work, nothing
// buffered) and reported tooLong so the caller can reject it and keep serving.
// The returned err is nil, io.EOF (trailing line without newline), or a read
// error; a non-nil err may still accompany a final partial line.
func readLine(r *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	for {
		chunk, e := r.ReadSlice('\n')
		if len(chunk) > 0 && !tooLong {
			if max > 0 && len(line)+len(chunk) > max {
				tooLong = true
				line = nil // stop buffering; keep draining to resync on the newline
			} else {
				line = append(line, chunk...)
			}
		}
		if e == bufio.ErrBufferFull {
			continue // more of this line remains in the reader
		}
		return line, tooLong, e
	}
}

// handleLine parses and dispatches one non-empty message. A parse failure is
// answered with -32700 and the stream continues; a leading '[' routes to batch
// handling.
func (s *Server) handleLine(w *bufio.Writer, line []byte) error {
	if line[0] == '[' {
		return s.handleBatch(w, line)
	}
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.log.Warn("decode request", "err", err)
		return s.writeFlush(w, errorResponse(nil, codeParseError, "parse error"))
	}
	resp, isNotif := s.serveOne(req)
	if isNotif {
		return nil
	}
	return s.writeFlush(w, resp)
}

// handleBatch processes a JSON-RPC batch: dispatch each member, collect the
// non-notification responses, and write them as one array (or nothing if every
// member was a notification). A structurally broken batch or member is rejected
// without ending the stream.
func (s *Server) handleBatch(w *bufio.Writer, line []byte) error {
	var raws []json.RawMessage
	if err := json.Unmarshal(line, &raws); err != nil {
		s.log.Warn("decode batch", "err", err)
		return s.writeFlush(w, errorResponse(nil, codeParseError, "parse error"))
	}
	if len(raws) == 0 {
		return s.writeFlush(w, errorResponse(nil, codeInvalidRequest, "empty batch"))
	}
	responses := make([]rpcResponse, 0, len(raws))
	for _, raw := range raws {
		var req rpcRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			responses = append(responses, errorResponse(nil, codeInvalidRequest, "invalid request in batch"))
			continue
		}
		resp, isNotif := s.serveOne(req)
		if isNotif {
			continue
		}
		responses = append(responses, resp)
	}
	if len(responses) == 0 {
		return nil // an all-notification batch gets no reply
	}
	return s.writeArrayFlush(w, responses)
}

// serveOne dispatches one request with a top-level recover so a panic anywhere
// in dispatch (outside a tool handler, which recovers to cave_tool_panicked) is
// contained: the request is answered with an internal error and the loop lives.
func (s *Server) serveOne(req rpcRequest) (resp rpcResponse, isNotif bool) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("dispatch panicked", "method", req.Method, "panic", r)
			if isNotification(req.ID) {
				resp, isNotif = rpcResponse{}, true
				return
			}
			resp, isNotif = errorResponse(req.ID, codeInternalError, "cave_internal_error"), false
		}
	}()
	return s.dispatch(req)
}

func (s *Server) write(w *bufio.Writer, resp rpcResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	return w.WriteByte('\n')
}

func (s *Server) writeFlush(w *bufio.Writer, resp rpcResponse) error {
	if err := s.write(w, resp); err != nil {
		return err
	}
	return w.Flush()
}

func (s *Server) writeArrayFlush(w *bufio.Writer, resps []rpcResponse) error {
	b, err := json.Marshal(resps)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}

func (s *Server) dispatch(req rpcRequest) (rpcResponse, bool) {
	switch req.Method {
	case "notifications/initialized", "notifications/cancelled":
		return rpcResponse{}, true
	}
	// A request with no usable id is a JSON-RPC notification: never answered.
	if isNotification(req.ID) {
		return rpcResponse{}, true
	}
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req), false
	case "ping":
		return successResponse(req.ID, map[string]any{}), false
	case "tools/list":
		return successResponse(req.ID, map[string]any{"tools": s.toolDefinitions()}), false
	case "tools/call":
		return s.handleToolCall(req), false
	default:
		return errorResponse(req.ID, codeMethodNotFound, "method not found: "+req.Method), false
	}
}

// handleInitialize negotiates the protocol version the way the MCP lifecycle
// requires: echo the client's version when this adapter supports it, otherwise
// answer with a version it DOES support and let the client decide whether to
// continue. Returning an error here instead is what the adapter used to do, and
// it made the server unusable with every client that has moved past
// 2024-11-05 — Claude Code among them, which reported
// "-32602: unsupported protocol version" and dropped the server.
//
// That failure is worse than a dead tool. `caveman wrap` decides whether the
// proxy may elide content from a marker file written at install time, not from
// the running agent, so the proxy went on compressing while the agent had no
// caveman_retrieve to expand what was elided. The conservative instinct behind
// the old code is kept: this adapter still never claims semantics it does not
// implement — it declines to echo, rather than refusing to speak.
func (s *Server) handleInitialize(req rpcRequest) rpcResponse {
	version := defaultProtocolVersion
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
		if _, ok := supportedProtocolVersions[p.ProtocolVersion]; ok {
			version = p.ProtocolVersion
		}
	}
	return successResponse(req.ID, map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": s.name, "version": s.version},
	})
}

func (s *Server) handleToolCall(req rpcRequest) rpcResponse {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResponse(req.ID, codeInvalidParams, "invalid tool-call params")
	}
	h, ok := s.index[p.Name]
	if !ok {
		// Unknown tool: a fail-closed tool error, never a fabricated result.
		return successResponse(req.ID, ToolError("cave_unknown_tool", "unknown tool: "+p.Name))
	}
	res := s.invokeHandler(p.Name, h, p.Arguments)
	// The cap guards unbounded generated output (compress/toon). Recovery tools
	// are exempt: they return the exact original bytes, which must never fail
	// closed on size or elided content becomes unrecoverable (#139).
	if s.maxResultBytes > 0 && !s.capExempt[p.Name] {
		if n := toolResultSize(res); n > s.maxResultBytes {
			s.log.Warn("tool result exceeds cap", "tool", p.Name, "bytes", n, "cap", s.maxResultBytes)
			res = ToolError("cave_payload_too_large",
				fmt.Sprintf("result %d bytes exceeds cap %d; request a narrower slice", n, s.maxResultBytes))
		}
	}
	return successResponse(req.ID, res)
}

// invokeHandler runs one tool handler under recover so a panic becomes a
// fail-closed cave_tool_panicked ToolError instead of crashing the process.
func (s *Server) invokeHandler(name string, h Handler, args json.RawMessage) (res ToolResult) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("tool handler panicked", "tool", name, "panic", r)
			res = ToolError("cave_tool_panicked", "tool handler panicked")
		}
	}()
	return h(args)
}

// toolResultSize is the total content-text length a host would render.
func toolResultSize(res ToolResult) int {
	n := 0
	for _, c := range res.Content {
		n += len(c.Text)
	}
	return n
}

func (s *Server) toolDefinitions() []map[string]any {
	out := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		definition := map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		}
		if len(t.Meta) > 0 {
			definition["_meta"] = t.Meta
		}
		out = append(out, definition)
	}
	return out
}
