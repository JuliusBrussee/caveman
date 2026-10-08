package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// TestReleaseShapeSubscriptionReadOfTypeScriptCompresses replays the #1020
// report through the real engine: a Claude Code subscription turn whose live
// tool_result is a TypeScript file as the Read tool prints it. Under
// CGO_ENABLED=0 (every release up to bin-v2.0.2) the row says eligible with
// zero tokens compressed, which is exactly what the reporter's counters showed.
// No build tag on purpose; the release-shape CI lane runs it with
// scripts/build-release-binaries.mjs's own flags.
func TestReleaseShapeSubscriptionReadOfTypeScriptCompresses(t *testing.T) {
	t.Setenv("CAVE_ENGINE_TOON", "")
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "engine", "evals", "fixtures", "sample.ts"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(source), "\n"), "\n")
	for i := range lines {
		lines[i] = strconv.Itoa(i+1) + "\t" + lines[i]
	}
	listing := strings.Join(lines, "\n") + "\n"

	turn1 := strings.Repeat("turn one project context ", 30)
	body, err := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 1024,
		"system":     []any{map[string]any{"type": "text", "text": "You are Claude Code.", "cache_control": map[string]any{"type": "ephemeral"}}},
		"tools":      []any{map[string]any{"name": "Read", "description": "Read a file", "input_schema": map[string]any{"type": "object"}}},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": turn1, "cache_control": map[string]any{"type": "ephemeral"}}}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "toolu_read", "name": "Read", "input": map[string]any{"file_path": "/repo/src/inventory.ts"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_read", "content": listing},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	recovery, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open ccr: %v", err)
	}
	defer recovery.Close()
	comp := &chatGPTRealEngineCompressor{eng: engine.New(recovery, nil), store: recovery}
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	srv, sink := newSubscriptionCompressServer(comp, rt, Config{RecoveryViaMCP: true})

	serveBody(t, srv, "/v1/messages", string(body), subscriptionAgentHeaders)

	row := sink.last(t)
	if !row.CompressionEligible {
		t.Fatalf("a subscription Read turn must reach the compression path: %+v", row)
	}
	if row.CompressionTokensBefore <= 0 {
		t.Fatalf("eligible but nothing compressed: the TypeScript Read listing passed through (this build has no tree-sitter code compressor): %+v", row)
	}
	if strings.Contains(string(rt.bodies[0]), "throw new Error(\\\"inventory: no items\\\")") {
		t.Fatalf("function body reached upstream unelided: %s", rt.bodies[0])
	}
}
