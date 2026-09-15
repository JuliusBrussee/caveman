package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMCPSurfaceParsesObservedClaudeJSONShapesAndSharesPrefixWithoutAttribution(t *testing.T) {
	cwd := t.TempDir()
	croot := t.TempDir()
	global := filepath.Join(t.TempDir(), ".claude.json")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{"project-a":{"command":"a"}}}`)
	write(filepath.Join(croot, ".mcp.json"), `{"mcpServers":{"root-b":{"type":"http","url":"https://example.invalid"}}}`)
	// json.Marshal the project key: on Windows cwd carries backslashes, which
	// raw concatenation would inject as invalid JSON escapes.
	cwdKey, err := json.Marshal(cwd)
	if err != nil {
		t.Fatal(err)
	}
	write(global, `{"mcpServers":{"context7":{"type":"http","url":"https://example.invalid"}},"projects":{`+string(cwdKey)+`:{"mcpServers":{"figma":{"command":"figma"}}}}}`)

	scopes := scanMCPConfigs(cwd, croot, global)
	cfg := configScan{MCPScopes: scopes}
	baseline := Sink{SinkID: "config_tax:baseline", Evidence: map[string]any{"unexplained_prefix_tokens": 700}}
	sinks := mcpSurfaceSink(cfg, []Sink{baseline})
	if len(sinks) != 1 {
		t.Fatalf("MCP sinks = %d, want 1: %+v", len(sinks), sinks)
	}
	sink := sinks[0]
	if sink.Evidence["server_count"] != 4 {
		t.Fatalf("server count = %v, want 4", sink.Evidence["server_count"])
	}
	wantServers := []string{"context7", "figma", "project-a", "root-b"}
	if !reflect.DeepEqual(sink.Evidence["servers"], wantServers) {
		t.Fatalf("servers = %#v, want %#v", sink.Evidence["servers"], wantServers)
	}
	if sink.Evidence["unexplained_prefix_tokens"] != 700 || sink.Evidence["unexplained_prefix_tokens_attribution"] != "shared_not_attributed" {
		t.Fatalf("shared prefix evidence = %+v", sink.Evidence)
	}
}
