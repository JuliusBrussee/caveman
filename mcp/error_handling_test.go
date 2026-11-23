package mcp

import (
	"strings"
	"testing"
)

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

func TestCompressMissingRequiredInputReturnsStructuredError(t *testing.T) {
	eng := mockEngine{}
	resps, _ := run(t, eng, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_compress","arguments":{}}}`)
	tr := decodeTool(t, resps[0].Result)
	if tr.IsError {
		if !strings.Contains(tr.Content[0].Text, "cave_") {
			t.Fatalf("error must carry a cave_snake_code, got %q", tr.Content[0].Text)
		}
	}
}

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

func TestToonDecodeInvalidInputReturnsError(t *testing.T) {
	resps, _ := run(t, realEngine(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_toon_decode","arguments":{"input":"not valid toon at all {{{"}}`)
	if len(resps) == 0 {
		t.Fatal("expected a response")
	}
	if resps[0].Error != nil {
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

func TestUnknownToolReturnsJSONRPCError(t *testing.T) {
	eng := mockEngine{}
	resps, _ := run(t, eng, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nonexistent_tool","arguments":{}}}`)
	if len(resps) == 0 {
		t.Fatal("expected a response")
	}
	if resps[0].Error == nil {
		tr := decodeTool(t, resps[0].Result)
		if !tr.IsError {
			t.Fatal("unknown tool must return an error (JSON-RPC or tool-level)")
		}
	}
}

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
