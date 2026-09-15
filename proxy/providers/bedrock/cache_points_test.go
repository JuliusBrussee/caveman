package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/awssig"
)

func cachePointsEnabled() providers.TransformPolicy {
	return providers.TransformPolicy{
		RuntimeMode: "active",
		Optimizers:  map[string]bool{CachePointsOptimizerID: true},
	}
}

func applyCachePoints(
	t *testing.T,
	body string,
	meta providers.RequestMetadata,
	policy providers.TransformPolicy,
) providers.TransformResult {
	t.Helper()
	adapter := New(stubBase).(Adapter)
	result, err := adapter.ApplyProviderNativeTransforms(
		context.Background(),
		strings.NewReader(body),
		meta,
		policy,
	)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}
	return result
}

func decodeCachePointBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	return root
}

func TestCachePointsConverseAddsToolCheckpointUpstreamOnly(t *testing.T) {
	body := `{"system":[{"text":"stable system"}],"toolConfig":{"tools":[{"toolSpec":{"name":"lookup","inputSchema":{"json":{"type":"object"}}}}]},"messages":[{"role":"user","content":[{"text":"hello"}]}]}`
	input := []byte(body)
	original := bytes.Clone(input)
	meta := providers.RequestMetadata{
		Provider: "bedrock",
		Model:    claudeModel,
		Endpoint: "converse",
	}
	adapter := New(stubBase).(Adapter)
	result, err := adapter.ApplyProviderNativeTransforms(
		context.Background(),
		bytes.NewReader(input),
		meta,
		cachePointsEnabled(),
	)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}

	if string(result.Body) == body {
		t.Fatal("enabled transform did not add upstream cachePoint")
	}
	if len(result.OptimizerIDs) != 1 || result.OptimizerIDs[0] != CachePointsOptimizerID {
		t.Fatalf("optimizer ids=%v, want [%s]", result.OptimizerIDs, CachePointsOptimizerID)
	}
	root := decodeCachePointBody(t, result.Body)
	tools := root["toolConfig"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools=%d, want original tool plus cachePoint", len(tools))
	}
	cachePoint := tools[1].(map[string]any)["cachePoint"].(map[string]any)
	if cachePoint["type"] != "default" {
		t.Fatalf("cachePoint=%v, want default", cachePoint)
	}
	if !bytes.Equal(input, original) {
		t.Fatal("client-side input buffer mutated")
	}
}

func TestCachePointsConverseFallsBackToSystemCheckpoint(t *testing.T) {
	body := `{"system":[{"text":"stable system"}],"messages":[{"role":"user","content":[{"text":"hello"}]}]}`
	result := applyCachePoints(t, body, providers.RequestMetadata{
		Provider: "bedrock",
		Model:    claudeModel,
		Endpoint: "converse-stream",
	}, cachePointsEnabled())

	root := decodeCachePointBody(t, result.Body)
	system := root["system"].([]any)
	if len(system) != 2 {
		t.Fatalf("system=%d, want original block plus cachePoint", len(system))
	}
	if got := system[1].(map[string]any)["cachePoint"].(map[string]any)["type"]; got != "default" {
		t.Fatalf("cachePoint type=%v, want default", got)
	}
}

func TestCachePointsAnthropicInvokeAddsCacheControl(t *testing.T) {
	body := `{"anthropic_version":"bedrock-2023-05-31","system":"stable system","tools":[{"name":"lookup","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hello"}],"max_tokens":64}`
	result := applyCachePoints(t, body, providers.RequestMetadata{
		Provider: "bedrock",
		Model:    claudeModel,
		Endpoint: "invoke-with-response-stream",
	}, cachePointsEnabled())

	root := decodeCachePointBody(t, result.Body)
	tools := root["tools"].([]any)
	control := tools[0].(map[string]any)["cache_control"].(map[string]any)
	if control["type"] != "ephemeral" {
		t.Fatalf("cache_control=%v, want ephemeral", control)
	}
}

func TestCachePointsUnsupportedVendorAndSurfaceAreByteIdentical(t *testing.T) {
	tests := []struct {
		name string
		body string
		meta providers.RequestMetadata
	}{
		{
			name: "non-anthropic-converse",
			body: `{"system":[{"text":"stable"}],"messages":[]}`,
			meta: providers.RequestMetadata{Provider: "bedrock", Model: "amazon.nova-pro-v1:0", Endpoint: "converse"},
		},
		{
			name: "non-anthropic-invoke",
			body: `{"system":"stable","messages":[]}`,
			meta: providers.RequestMetadata{Provider: "bedrock", Model: "meta.llama3-70b-instruct-v1:0", Endpoint: "invoke"},
		},
		{
			name: "cross-region-profile-not-proven",
			body: `{"system":[{"text":"stable"}],"messages":[]}`,
			meta: providers.RequestMetadata{Provider: "bedrock", Model: "us." + claudeModel, Endpoint: "converse"},
		},
		{
			// Review H2: the bare `anthropic.claude-` prefix admitted legacy
			// models with no catalog prompt_cache row; injecting there risks
			// converting working traffic into upstream rejections. Eligibility
			// now requires the catalog capability, so this passes through.
			name: "prefix-matching-model-without-catalog-prompt-cache",
			body: `{"system":[{"text":"stable"}],"messages":[]}`,
			meta: providers.RequestMetadata{Provider: "bedrock", Model: "anthropic.claude-v2:1", Endpoint: "converse"},
		},
		{
			name: "mantle-deferred-to-c4",
			body: `{"model":"claude-sonnet-4-6","system":"stable","messages":[]}`,
			meta: providers.RequestMetadata{Provider: "bedrock", Model: claudeModel, Endpoint: "mantle_messages"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := applyCachePoints(t, test.body, test.meta, cachePointsEnabled())
			if len(result.OptimizerIDs) != 0 || string(result.Body) != test.body {
				t.Fatalf("unsupported request changed: ids=%v body=%s", result.OptimizerIDs, result.Body)
			}
		})
	}
}

func TestCachePointsRespectCallerMarkersByteIdentically(t *testing.T) {
	tests := []struct {
		name string
		body string
		meta providers.RequestMetadata
	}{
		{
			name: "converse-cachePoint",
			body: `{"system":[{"text":"stable"},{"cachePoint":{"type":"default"}}],"messages":[]}`,
			meta: providers.RequestMetadata{Provider: "bedrock", Model: claudeModel, Endpoint: "converse"},
		},
		{
			name: "invoke-cache-control",
			body: `{"system":[{"type":"text","text":"stable","cache_control":{"type":"ephemeral"}}],"messages":[]}`,
			meta: providers.RequestMetadata{Provider: "bedrock", Model: claudeModel, Endpoint: "invoke"},
		},
		{
			name: "unicode-escaped-cache-control",
			body: `{"system":[{"type":"text","text":"stable","cache\u005fcontrol":{"type":"ephemeral"}}],"messages":[]}`,
			meta: providers.RequestMetadata{Provider: "bedrock", Model: claudeModel, Endpoint: "invoke"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := applyCachePoints(t, test.body, test.meta, cachePointsEnabled())
			if len(result.OptimizerIDs) != 0 || string(result.Body) != test.body {
				t.Fatalf("caller marker changed: ids=%v body=%s", result.OptimizerIDs, result.Body)
			}
		})
	}
}

func TestCachePointsDisabledNonPAYGAndMalformedPassThrough(t *testing.T) {
	body := `{"system":[{"text":"stable"}],"messages":[]}`
	meta := providers.RequestMetadata{Provider: "bedrock", Model: claudeModel, Endpoint: "converse"}
	policies := []providers.TransformPolicy{
		{RuntimeMode: "active", Optimizers: map[string]bool{}},
		{RuntimeMode: "record", Optimizers: map[string]bool{CachePointsOptimizerID: true}},
		{RuntimeMode: "active", AuthMode: "subscription", Optimizers: map[string]bool{CachePointsOptimizerID: true}},
	}
	for _, policy := range policies {
		result := applyCachePoints(t, body, meta, policy)
		if len(result.OptimizerIDs) != 0 || string(result.Body) != body {
			t.Fatalf("gated request changed: ids=%v body=%s", result.OptimizerIDs, result.Body)
		}
	}

	malformed := `{"system":[}`
	result := applyCachePoints(t, malformed, meta, cachePointsEnabled())
	if len(result.OptimizerIDs) != 0 || string(result.Body) != malformed {
		t.Fatalf("malformed request changed: ids=%v body=%s", result.OptimizerIDs, result.Body)
	}

	malformedShape := `{"toolConfig":{"tools":["not-a-tool"]},"system":[{"text":"stable"}],"messages":[]}`
	result = applyCachePoints(t, malformedShape, meta, cachePointsEnabled())
	if len(result.OptimizerIDs) != 0 || string(result.Body) != malformedShape {
		t.Fatalf("malformed Converse shape changed: ids=%v body=%s", result.OptimizerIDs, result.Body)
	}
}

func TestCachePointsSigV4HashesExactTransformedWireBody(t *testing.T) {
	body := `{"system":[{"text":"stable system"}],"messages":[]}`
	meta := providers.RequestMetadata{Provider: "bedrock", Model: claudeModel, Endpoint: "converse"}
	result := applyCachePoints(t, body, meta, cachePointsEnabled())

	adapter := newAdapter(t)
	request, _ := http.NewRequest(http.MethodPost, invokePath(claudeModel, "converse"), nil)
	upstream, err := adapter.ResolveUpstreamURL(context.Background(), request, providers.RouteContext{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := providers.WithRequestPayloadHash(context.Background(), result.Body)
	headers, err := adapter.SanitizeAndMapHeaders(ctx, request, providers.Credential{
		Key:      "AKIAIOSFODNN7EXAMPLE:" + testSecret,
		AuthKind: "aws_access_keys",
	}, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := headers.Get("X-Amz-Content-Sha256"), awssig.HashPayload(result.Body); got != want {
		t.Fatalf("payload hash=%q, want transformed wire hash %q", got, want)
	}
}

// TestCachePointEligibilityCoversInferenceProfiles pins the population fix
// (2026-08-02): the router strips global./us./eu. routing scopes before its
// Claude-prefix allowlist, and the cache-point eligibility predicate must
// apply the SAME normalization — otherwise catalog-priced modern global.*
// profile rows are routed but structurally uninjectable, shrinking the
// mintable population to the two legacy us-east-1 models. Eligibility still
// keys on the EXACT catalog model id: a profile id with no catalog row of its
// own (us./eu. geographic profiles carry different AWS pricing and no
// grounded rate exists) stays out — honest zero, never a borrowed rate.
func TestCachePointEligibilityCoversInferenceProfiles(t *testing.T) {
	for model, want := range map[string]bool{
		// Catalog-priced global profiles with prompt_cache + full cache rates.
		"global.anthropic.claude-opus-4-8":                true,
		"global.anthropic.claude-sonnet-4-6":              true,
		"global.anthropic.claude-haiku-4-5-20251001-v1:0": true,
		// Legacy bare ids, unchanged.
		"anthropic.claude-3-5-sonnet-20241022-v2:0": true,
		"anthropic.claude-3-5-haiku-20241022-v1:0":  true,
		// Geographic profiles: routed by the allowlist, but no catalog row of
		// their own => not eligible (fail closed).
		"us.anthropic.claude-sonnet-4-6": false,
		"eu.anthropic.claude-sonnet-4-6": false,
		// Non-Claude vendors never become eligible even with prompt_cache rows.
		"global.amazon.nova-2-lite-v1:0":            false,
		"us.meta.llama4-maverick-17b-instruct-v1:0": false,
		// Unknown/absent ids fail closed.
		"global.anthropic.claude-imaginary-9": false,
	} {
		if got := CachePointEligibleModel(model); got != want {
			t.Errorf("CachePointEligibleModel(%q) = %v, want %v", model, got, want)
		}
	}
}

// TestCachePointsInjectsForGlobalInferenceProfile proves the widened
// population end to end at the transform: a Converse body carrying a modern
// global.* profile id gains exactly one cachePoint, while a geographic
// profile id (no catalog row) passes through byte-identically.
func TestCachePointsInjectsForGlobalInferenceProfile(t *testing.T) {
	body := `{"system":[{"text":"stable system"}],"messages":[{"role":"user","content":[{"text":"hello"}]}]}`
	result := applyCachePoints(t, body, providers.RequestMetadata{
		Provider: "bedrock",
		Model:    "global.anthropic.claude-sonnet-4-6",
		Endpoint: "converse",
	}, cachePointsEnabled())
	if len(result.OptimizerIDs) != 1 || result.OptimizerIDs[0] != CachePointsOptimizerID {
		t.Fatalf("optimizer ids = %v, want [%s]", result.OptimizerIDs, CachePointsOptimizerID)
	}
	if !bytes.Contains(result.Body, []byte("cachePoint")) {
		t.Fatal("no cachePoint injected for a catalog-priced global profile")
	}

	passthrough := applyCachePoints(t, body, providers.RequestMetadata{
		Provider: "bedrock",
		Model:    "us.anthropic.claude-sonnet-4-6",
		Endpoint: "converse",
	}, cachePointsEnabled())
	if len(passthrough.OptimizerIDs) != 0 || string(passthrough.Body) != body {
		t.Fatal("a geographic profile with no catalog row must pass through byte-identically")
	}
}
