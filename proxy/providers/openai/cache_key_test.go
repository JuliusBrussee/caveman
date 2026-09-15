package openai

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func enabled() providers.TransformPolicy {
	return providers.TransformPolicy{RuntimeMode: "active", Optimizers: map[string]bool{OptimizerID: true}}
}

func apply(t *testing.T, body string, policy providers.TransformPolicy) providers.TransformResult {
	return applyAtEndpoint(t, body, "", policy)
}

func applyAtEndpoint(t *testing.T, body, endpoint string, policy providers.TransformPolicy) providers.TransformResult {
	t.Helper()
	a := New("http://upstream").(Adapter)
	res, err := a.ApplyProviderNativeTransforms(context.Background(), strings.NewReader(body), providers.RequestMetadata{Provider: "openai", Endpoint: endpoint}, policy)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}
	return res
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	return m
}

func TestPromptCacheKey_ToolsGetKey(t *testing.T) {
	body := `{"model":"gpt-5.5","tools":[{"type":"function","function":{"name":"a"}}],"messages":[{"role":"user","content":"hi"}]}`
	res := apply(t, body, enabled())

	if len(res.OptimizerIDs) != 1 || res.OptimizerIDs[0] != OptimizerID {
		t.Fatalf("optimizer ids = %v, want [%s]", res.OptimizerIDs, OptimizerID)
	}
	root := decode(t, res.Body)
	key, ok := root["prompt_cache_key"].(string)
	if !ok || len(key) != 32 {
		t.Fatalf("expected a 32-char prompt_cache_key, got %v", root["prompt_cache_key"])
	}
	// Byte-safe: only prompt_cache_key is added; every original field is preserved.
	assertOnlyKeyAdded(t, body, root)
}

func TestPromptCacheKey_SystemMessageGetsKey(t *testing.T) {
	body := `{"model":"gpt-5.5","messages":[{"role":"system","content":"You are careful."},{"role":"user","content":"hi"}]}`
	res := apply(t, body, enabled())
	if len(res.OptimizerIDs) != 1 {
		t.Fatalf("expected optimizer applied for a system-prefixed request, got %v", res.OptimizerIDs)
	}
	assertOnlyKeyAdded(t, body, decode(t, res.Body))
}

func TestPromptCacheKey_InstructionsGetKey(t *testing.T) {
	// Responses API shape: top-level instructions is the system equivalent.
	body := `{"model":"gpt-5.5","instructions":"Answer tersely.","input":"hi"}`
	res := apply(t, body, enabled())
	if len(res.OptimizerIDs) != 1 {
		t.Fatalf("expected optimizer applied for instructions, got %v", res.OptimizerIDs)
	}
}

func TestPromptCacheKey_StableAcrossWhitespaceAndKeyOrder(t *testing.T) {
	// Same logical prefix, different byte form -> identical key (canonical hash).
	a := apply(t, `{"model":"gpt-5.5","tools":[{"type":"function","function":{"name":"a"}}],"messages":[{"role":"user","content":"x"}]}`, enabled())
	b := apply(t, `{"messages":[{"content":"y","role":"user"}],"tools":[{"function":{"name":"a"},"type":"function"}],"model":"gpt-5.5"}`, enabled())
	ka := decode(t, a.Body)["prompt_cache_key"]
	kb := decode(t, b.Body)["prompt_cache_key"]
	if ka != kb {
		t.Errorf("same logical prefix must produce the same key: %v vs %v", ka, kb)
	}
}

func TestPromptCacheKey_DistinctPrefixesDistinctKeys(t *testing.T) {
	a := apply(t, `{"model":"gpt-5.5","tools":[{"function":{"name":"a"}}],"messages":[]}`, enabled())
	b := apply(t, `{"model":"gpt-5.5","tools":[{"function":{"name":"b"}}],"messages":[]}`, enabled())
	if decode(t, a.Body)["prompt_cache_key"] == decode(t, b.Body)["prompt_cache_key"] {
		t.Error("different tool prefixes must produce different keys")
	}
}

func TestPromptCacheKey_DisabledIsPassthrough(t *testing.T) {
	body := `{"model":"gpt-5.5","tools":[{"function":{"name":"a"}}],"messages":[]}`
	res := apply(t, body, providers.TransformPolicy{RuntimeMode: "active", Optimizers: map[string]bool{}})
	if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
		t.Errorf("disabled optimizer must pass body through unchanged, got ids=%v", res.OptimizerIDs)
	}
}

func TestPromptCacheKey_RespectsExistingKey(t *testing.T) {
	body := `{"model":"gpt-5.5","prompt_cache_key":"caller-set","tools":[{"function":{"name":"a"}}],"messages":[]}`
	res := apply(t, body, enabled())
	if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
		t.Errorf("existing prompt_cache_key must be respected (passthrough), got ids=%v", res.OptimizerIDs)
	}
}

func TestPromptCacheKey_NoStablePrefixOrBadJSON(t *testing.T) {
	// Plain messages, no tools/system -> nothing stable to key on.
	res := apply(t, `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`, enabled())
	if len(res.OptimizerIDs) != 0 {
		t.Errorf("no stable prefix should be passthrough, got %v", res.OptimizerIDs)
	}
	// Invalid JSON -> passthrough, no error.
	bad := apply(t, `not json`, enabled())
	if len(bad.OptimizerIDs) != 0 || string(bad.Body) != "not json" {
		t.Errorf("invalid JSON must pass through unchanged")
	}
}

func TestPromptCacheKey_Idempotent(t *testing.T) {
	once := apply(t, `{"model":"gpt-5.5","tools":[{"function":{"name":"a"}}],"messages":[]}`, enabled())
	twice := apply(t, string(once.Body), enabled())
	if len(twice.OptimizerIDs) != 0 {
		t.Errorf("re-applying should be idempotent (key already present), got %v", twice.OptimizerIDs)
	}
}

func TestPromptCacheKey_PreservesOriginalRawBytes(t *testing.T) {
	body := "{\n \"model\" : \"gpt-5.5\", \"tools\" : [{\"function\":{\"name\":\"a\",\"description\":\"<>&\"}}], \"messages\" : []\n}"
	res := apply(t, body, enabled())
	key := decode(t, res.Body)["prompt_cache_key"].(string)
	want := strings.Replace(body, "\n}", `,"prompt_cache_key":"`+key+"\"\n}", 1)
	if string(res.Body) != want {
		t.Fatalf("cache-key insertion reserialized untouched bytes:\n got %s\nwant %s", res.Body, want)
	}
}

// assertOnlyKeyAdded verifies the transform is semantically additive: the
// decoded result equals the decoded original plus exactly the prompt_cache_key.
func assertOnlyKeyAdded(t *testing.T, original string, result map[string]any) {
	t.Helper()
	var orig map[string]any
	if err := json.Unmarshal([]byte(original), &orig); err != nil {
		t.Fatalf("original not valid JSON: %v", err)
	}
	if _, ok := result["prompt_cache_key"]; !ok {
		t.Fatal("result missing prompt_cache_key")
	}
	stripped := map[string]any{}
	for k, v := range result {
		if k == "prompt_cache_key" {
			continue
		}
		stripped[k] = v
	}
	if !reflect.DeepEqual(orig, stripped) {
		t.Errorf("transform changed model-visible content.\n original: %v\n result-minus-key: %v", orig, stripped)
	}
}
