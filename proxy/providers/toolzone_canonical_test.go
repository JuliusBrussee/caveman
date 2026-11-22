package providers

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCanonicalizeToolCatalogAnthropicPreservesTrailingCacheControl(t *testing.T) {
	// Shuffled tools with Anthropic trailing cache_control on the last tool.
	raw := []byte(`[
		{"name":"mcp__linear__list_issues","description":"List issues","input_schema":{"type":"object"}},
		{"name":"Bash","description":"Run command","input_schema":{"type":"object"}},
		{"name":"Read","description":"Read file","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral","ttl":"1h"}}
	]`)
	meta := RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}
	out, changed := CanonicalizeToolCatalog(raw, meta)
	if !changed {
		t.Fatalf("expected unsorted catalog to be canonicalized")
	}

	var tools []map[string]any
	if err := json.Unmarshal(out, &tools); err != nil {
		t.Fatalf("canonical output invalid JSON: %v\n%s", err, out)
	}
	if len(tools) != 3 {
		t.Fatalf("len(tools)=%d want 3", len(tools))
	}
	gotOrder := []string{tools[0]["name"].(string), tools[1]["name"].(string), tools[2]["name"].(string)}
	if gotOrder[0] != "Bash" || gotOrder[1] != "Read" || gotOrder[2] != "mcp__linear__list_issues" {
		t.Fatalf("unexpected sorted order: %v", gotOrder)
	}
	// cache_control MUST have migrated onto the NEW last element only!
	if _, has := tools[1]["cache_control"]; has {
		t.Fatalf("old last tool (Read) should not retain cache_control after sort: %s", out)
	}
	cc, ok := tools[2]["cache_control"].(map[string]any)
	if !ok || cc["ttl"] != "1h" {
		t.Fatalf("new last tool must receive exact migrated cache_control, got: %s", out)
	}

	// Idempotent: second call returns changed=false.
	if _, changedAgain := CanonicalizeToolCatalog(out, meta); changedAgain {
		t.Fatalf("already-sorted catalog must return changed=false")
	}

	// Shuffled in a DIFFERENT order with unsorted JSON keys and raw <>& chars
	// MUST produce 100% BYTE-IDENTICAL canonical output (no json.Marshal drift!).
	rawPermA := []byte(`[` +
		`{"description":"List <issues> & PRs","name":"mcp__linear__list_issues","input_schema":{"type":"object"}},` +
		`{"description":"Run bash > cmd","name":"Bash","input_schema":{"type":"object"}},` +
		`{"description":"Read file","name":"Read","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral","ttl":"1h"}}` +
		`]`)
	rawPermB := []byte(`[` +
		`{"description":"Read file","name":"Read","input_schema":{"type":"object"}},` +
		`{"description":"List <issues> & PRs","name":"mcp__linear__list_issues","input_schema":{"type":"object"}},` +
		`{"description":"Run bash > cmd","name":"Bash","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral","ttl":"1h"}}` +
		`]`)
	outA, changedA := CanonicalizeToolCatalog(rawPermA, meta)
	outB, changedB := CanonicalizeToolCatalog(rawPermB, meta)
	if !changedA || !changedB {
		t.Fatalf("expected both permutations to canonicalize")
	}
	if !bytes.Equal(outA, outB) {
		t.Fatalf("permutations produced different wire bytes (self-inflicted cache bust!):\nA: %s\nB: %s", outA, outB)
	}
	if !bytes.Contains(outA, []byte(`<issues> & PRs`)) {
		t.Fatalf("byte-exact splice must preserve unescaped <>& characters: %s", outA)
	}
}

func TestCanonicalizeToolCatalogAnthropicSkipsDeferredToolForCacheControl(t *testing.T) {
	// Anthropic forbids cache_control on a tool with defer_loading: true.
	raw := []byte(`[
		{"name":"mcp__zendesk__get_ticket","defer_loading":true,"input_schema":{"type":"object"}},
		{"name":"Read","input_schema":{"type":"object"}},
		{"name":"Bash","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}
	]`)
	out, changed := CanonicalizeToolCatalog(raw, RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"})
	if !changed {
		t.Fatalf("expected catalog to sort")
	}
	var tools []map[string]any
	if err := json.Unmarshal(out, &tools); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	// Sorted order: Bash, Read, mcp__zendesk__get_ticket (deferred).
	// cache_control MUST land on Read (last non-deferred), NEVER on mcp__zendesk__get_ticket!
	if _, hasCC := tools[2]["cache_control"]; hasCC {
		t.Fatalf("deferred tool must NOT receive cache_control: %s", out)
	}
	if _, hasCC := tools[1]["cache_control"]; !hasCC || tools[1]["name"] != "Read" {
		t.Fatalf("expected cache_control on last non-deferred tool Read, got: %s", out)
	}
}

func TestCanonicalizeToolCatalogBailsOnMultipleCacheControls(t *testing.T) {
	raw := []byte(`[
		{"name":"zeta","cache_control":{"type":"ephemeral"}},
		{"name":"alpha","cache_control":{"type":"ephemeral"}}
	]`)
	if _, changed := CanonicalizeToolCatalog(raw, RequestMetadata{Provider: "anthropic"}); changed {
		t.Fatalf("must bail out when multiple tools carry cache_control")
	}
}

func TestCanonicalizeToolCatalogOpenAIAndGemini(t *testing.T) {
	oaChat := []byte(`[{"type":"function","function":{"name":"zeta","parameters":{}}},{"type":"function","function":{"name":"alpha","parameters":{}}}]`)
	outOA, ok := CanonicalizeToolCatalog(oaChat, RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions"})
	if !ok {
		t.Fatalf("expected OpenAI Chat tools to sort")
	}
	names := ToolCatalogNames(outOA)
	if len(names) != 2 || names[0] != "f:alpha" || names[1] != "f:zeta" {
		t.Fatalf("unexpected OpenAI sorted names: %v", names)
	}

	geminiRaw := []byte(`[{"functionDeclarations":[{"name":"z_tool","description":"z"},{"name":"a_tool","description":"a"}]}]`)
	outGem, ok := CanonicalizeToolCatalog(geminiRaw, RequestMetadata{Provider: "gemini", Endpoint: "/v1beta/models/gemini-2.5-pro:generateContent"})
	if !ok {
		t.Fatalf("expected Gemini functionDeclarations to sort")
	}
	if !bytes.Contains(outGem, []byte(`"a_tool"`)) || bytes.Index(outGem, []byte(`"a_tool"`)) > bytes.Index(outGem, []byte(`"z_tool"`)) {
		t.Fatalf("Gemini functionDeclarations not sorted: %s", outGem)
	}

	// Already-sorted array with extra whitespace normalizes cleanly to match shuffled permutation bytes.
	spacedSorted := []byte(`[  {"name":"alpha","input_schema":{}} ,  {"name":"zeta","input_schema":{}}  ]`)
	compactShuffled := []byte(`[{"name":"zeta","input_schema":{}},{"name":"alpha","input_schema":{}}]`)
	outSpaced, _ := CanonicalizeToolCatalog(spacedSorted, RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"})
	outShuffled, _ := CanonicalizeToolCatalog(compactShuffled, RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"})
	if !bytes.Equal(outSpaced, outShuffled) {
		t.Fatalf("already-sorted spaced array must normalize to match shuffled canonical bytes:\nspaced:   %s\nshuffled: %s", outSpaced, outShuffled)
	}

	// OpenAI Responses mcp + namespace tools sort deterministically and tool_search pins first.
	respTools := []byte(`[` +
		`{"type":"mcp","server_label":"zendesk","server_url":"https://z.invalid"},` +
		`{"type":"namespace","name":"billing","tools":[{"type":"function","name":"z_pay"},{"type":"function","name":"a_invoice"}]},` +
		`{"type":"tool_search"},` +
		`{"type":"mcp","server_label":"alpha_mcp","server_url":"https://a.invalid"}` +
		`]`)
	outResp, changed := CanonicalizeToolCatalog(respTools, RequestMetadata{Provider: "openai", Endpoint: "/v1/responses"})
	if !changed {
		t.Fatalf("expected OpenAI Responses mcp/namespace/tool_search catalog to sort")
	}
	if bytes.Index(outResp, []byte(`"tool_search"`)) > bytes.Index(outResp, []byte(`"alpha_mcp"`)) ||
		bytes.Index(outResp, []byte(`"a_invoice"`)) > bytes.Index(outResp, []byte(`"z_pay"`)) {
		t.Fatalf("unexpected Responses canonical order: %s", outResp)
	}
}
