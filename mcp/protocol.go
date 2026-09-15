package mcp

import (
	"bytes"
	"encoding/json"
)

// JSON-RPC 2.0 error codes used by the server.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// rpcRequest is one inbound JSON-RPC request. A request whose ID is absent (or
// explicitly null) is a notification and gets no response — see isNotification.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification reports whether a request carries no usable id and therefore
// must never be answered. Per JSON-RPC 2.0 a notification is a request with no
// "id" member; a literal `"id":null` cannot correlate a response either, so both
// are treated as notifications (the id-less reply was sub-issue #4 of #139).
func isNotification(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	return bytes.Equal(bytes.TrimSpace(id), []byte("null"))
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func successResponse(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResponse(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// ToolResult is an MCP tools/call result: a content list plus an error flag.
type ToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError"`
}

type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolText wraps a value as a single JSON text content item (the agent receives
// JSON it can parse).
func ToolText(v any) ToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return ToolError("cave_marshal_failed", "could not encode result")
	}
	return ToolResult{Content: []ToolContent{{Type: "text", Text: string(b)}}, IsError: false}
}

// ToolRawText wraps a raw string (e.g. recovered original bytes) as text content.
func ToolRawText(s string) ToolResult {
	return ToolResult{Content: []ToolContent{{Type: "text", Text: s}}, IsError: false}
}

// ToolError is a fail-closed tool error: isError=true with a cave_snake_code, so
// a host never mistakes it for a successful payload.
func ToolError(code, msg string) ToolResult {
	b, _ := json.Marshal(map[string]string{"error": code, "message": msg})
	return ToolResult{Content: []ToolContent{{Type: "text", Text: string(b)}}, IsError: true}
}

// ObjectSchema builds a JSON-Schema object for a tool's inputSchema.
func ObjectSchema(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

// StringProp is a string property schema with a description.
func StringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
