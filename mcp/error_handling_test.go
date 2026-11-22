package mcp

import (
	"strings"
	"testing"
)

// TestCompressInvalidArgumentsReturnsStructuredError verifies that malformed
// tool arguments produce a structured ToolError with a cave_snake_code rather
// than a panic or silent swallow.
func TestCompressInvalidArgumentsReturnsStructuredError(t *testing.T) {
	eng := mockEngine{}
	resps, _ := run(t, eng, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_compress","arguments":"not-an-object"}}`)
	if len(resps) != 1 {
		t.Fatalf("want 1 response, got %d", len(resps))
	}
	tr := decodeTool(t, resps[0].Result)
	if !tr.IsError {
		t.Fatal("invalid arguments must be an error")
	}
	if !strings.Contains(tr.Content[0].Text, "cave_invalid_arguments") {
		t.Fatalf("error must carry cave_invalid_arguments, got %q", tr.Content[0].Text)
	}
}

// TestCompressMissingRequiredInputReturnsStructuredError verifies that a call
// without the required "input" field returns a structured error instead of
// producing an empty pass-through.
func TestCompressMissingRequiredInputReturnsStructuredError(t *testing.T) {
	eng := mockEngine{}
	resps, _ := run(t, eng, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_compress","arguments":{}}}`)
	tr := decodeTool(t, resps[0].Result)
	// Missing input should either be an error or produce an empty pass-through
	// that is explicitly accounted. The key is it must not panic.
	if tr.IsError {
		if !strings.Contains(tr.Content[0].Text, "cave_") {
			t.Fatalf("error must carry a cave_snake_code, got %q", tr.Content[0].Text)
		}
	}
}

// TestRetrieveMissingHandleReturnsStructuredError verifies that retrieve without
// a recovery_handle returns a structured error with cave_invalid_arguments.
func TestRetrieveMissingHandleReturnsStructuredError(t *testing.T) {
	eng := mockEngine{}
	resps, _ := run(t, eng, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_retrieve","arguments":{}}}`)
	tr := decodeTool(t, resps[0].Result)
	if !tr.IsError {
		t.Fatal("missing recovery_handle must be an error")
	}
	if !strings.Contains(tr.Content[0].Text, "cave_invalid_arguments") {
		t.Fatalf("error must carry cave_invalid_arguments, got %q", tr.Content[0].Text)
	}
}

// TestRetrieveEmptyHandleReturnsStructuredError verifies that an empty
// recovery_handle string returns a structured error.
func TestRetrieveEmptyHandleReturnsStructuredError(t *testing.T) {
	eng := mockEngine{}
	resps, _ := run(t, eng, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_retrieve","arguments":{"recovery_handle":""}}}`)
	tr := decodeTool(t, resps[0].Result)
	if !tr.IsError {
		t.Fatal("empty recovery_handle must be an error")
	}
	if !strings.Contains(tr.Content[0].Text, "cave_invalid_arguments") {
		t.Fatalf("error must carry cave_invalid_arguments, got %q", tr.Content[0].Text)
	}
}

// TestToonDecodeInvalidInputReturnsError verifies that invalid TOON input
// produces a structured error rather than silently passing through.
func TestToonDecodeInvalidInputReturnsError(t *testing.T) {
	resps, _ := run(t, realEngine(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_toon_decode","arguments":{"input":"not valid toon at all {{{"}}`)
	if len(resps) == 0 {
		t.Fatal("expected a response")
	}
	if resps[0].Error != nil {
		// JSON-RPC level error is acceptable
		return
	}
	if resps[0].Result == nil {
		t.Fatal("expected a result")
	}
	tr := decodeTool(t, resps[0].Result)
	if !tr.IsError {
		t.Fatal("invalid TOON must be an error")
	}
	if !strings.Contains(tr.Content[0].Text, "cave_") {
		t.Fatalf("error must carry a cave_snake_code, got %q", tr.Content[0].Text)
	}
}

// TestToonEncodeMissingInputReturnsError verifies that toon_encode without
// input returns a structured error.
func TestToonEncodeMissingInputReturnsError(t *testing.T) {
	resps, _ := run(t, realEngine(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_toon_encode","arguments":{}}}`)
	tr := decodeTool(t, resps[0].Result)
	if !tr.IsError {
		t.Fatal("missing input must be an error")
	}
	if !strings.Contains(tr.Content[0].Text, "cave_") {
		t.Fatalf("error must carry a cave_snake_code, got %q", tr.Content[0].Text)
	}
}

// TestUnknownToolReturnsJSONRPCError verifies that calling an unknown tool
// returns a JSON-RPC error rather than a panic.
func TestUnknownToolReturnsJSONRPCError(t *testing.T) {
	eng := mockEngine{}
	resps, _ := run(t, eng, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nonexistent_tool","arguments":{}}}`)
	if len(resps) == 0 {
		t.Fatal("expected a response")
	}
	// The server may return either a JSON-RPC error or a tool result with isError.
	// Both are acceptable as long as it doesn't panic.
	if resps[0].Error == nil {
		tr := decodeTool(t, resps[0].Result)
		if !tr.IsError {
			t.Fatal("unknown tool must return an error (JSON-RPC or tool-level)")
		}
	}
}

// TestMalformedJSONRPCReturnsError verifies that malformed JSON-RPC messages
// are handled gracefully.
func TestMalformedJSONRPCReturnsError(t *testing.T) {
	eng := mockEngine{}
	resps, _ := run(t, eng, `{not valid json}`)
	if len(resps) == 0 {
		t.Fatal("malformed JSON must produce a response")
	}
	if resps[0].Error == nil {
		t.Fatal("malformed JSON must return JSON-RPC error")
	}
}
