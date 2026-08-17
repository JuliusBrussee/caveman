package gemini

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func apply(t *testing.T, body string, policy providers.TransformPolicy) providers.TransformResult {
	t.Helper()
	a := New("http://upstream").(Adapter)
	res, err := a.ApplyProviderNativeTransforms(context.Background(), strings.NewReader(body), providers.RequestMetadata{Provider: "gemini"}, policy)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}
	return res
}

func enabled() providers.TransformPolicy {
	return providers.TransformPolicy{RuntimeMode: "active", Optimizers: map[string]bool{OptimizerID: true}}
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	return m
}

const sysReq = `{"systemInstruction":{"parts":[{"text":"You are a careful assistant with a long stable preamble."}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`

// RETIRED: even a stale enabled flag must not move systemInstruction into a
// cache reference, emit telemetry attribution, or change any request byte.
func TestExplicitCache_RetiredFlagIsByteIdenticalAndUnattributed(t *testing.T) {
	res := apply(t, sysReq, enabled())
	if len(res.OptimizerIDs) != 0 {
		t.Fatalf("retired optimizer must attribute nothing, got %v", res.OptimizerIDs)
	}
	if string(res.Body) != sysReq {
		t.Errorf("retired optimizer changed bytes:\n got %s\nwant %s", res.Body, sysReq)
	}
	// The system prompt (model-visible) must survive untouched.
	root := decode(t, res.Body)
	if _, present := root["systemInstruction"]; !present {
		t.Error("systemInstruction must NOT be removed while the cache is never created")
	}
}

func TestExplicitCache_DisabledIsPassThrough(t *testing.T) {
	res := apply(t, sysReq, providers.TransformPolicy{RuntimeMode: "active", Optimizers: map[string]bool{OptimizerID: false}})
	if len(res.OptimizerIDs) != 0 {
		t.Fatalf("disabled optimizer must not apply, got %v", res.OptimizerIDs)
	}
	if string(res.Body) != sysReq {
		t.Errorf("disabled must pass through unchanged")
	}
}

func TestExplicitCache_RecordModePassThrough(t *testing.T) {
	// The proxy never calls transforms in record mode; even if reached, an empty
	// optimizer map means OptimizerEnabled is false -> pass through.
	res := apply(t, sysReq, providers.TransformPolicy{RuntimeMode: "record", Optimizers: map[string]bool{}})
	if len(res.OptimizerIDs) != 0 || string(res.Body) != sysReq {
		t.Errorf("record/no-optimizer must pass through unchanged")
	}
}

func TestExplicitCache_IdempotentWhenAlreadyCached(t *testing.T) {
	body := `{"cachedContent":"cachedContents/already","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	res := apply(t, body, enabled())
	if len(res.OptimizerIDs) != 0 {
		t.Fatalf("must not re-cache a request that already references a cache")
	}
	if string(res.Body) != body {
		t.Errorf("already-cached request altered")
	}
}

func TestExplicitCache_NoSystemInstructionPassThrough(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	res := apply(t, body, enabled())
	if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
		t.Errorf("no stable prefix -> pass through")
	}
}

func TestExplicitCache_GarbagePassThrough(t *testing.T) {
	body := `{not json`
	res := apply(t, body, enabled())
	if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
		t.Errorf("unparseable body must pass through unchanged")
	}
}
