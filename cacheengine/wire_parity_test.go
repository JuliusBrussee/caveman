package cacheengine

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/bedrock"
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
)

func TestStandaloneAnthropicCompilerMatchesGatewayStableTransform(t *testing.T) {
	body := []byte(`{
 "model":"claude-sonnet-4-6",
 "tools":[{"name":"loaded"},{"name":"deferred","defer_loading":true}],
 "system":"stable",
 "messages":[{"role":"user","content":"question"}],
 "max_tokens":256
}`)
	want, err := anthropic.New("https://api.anthropic.com").ApplyProviderNativeTransforms(
		context.Background(), bytes.NewReader(body),
		providers.RequestMetadata{Provider: "anthropic", Model: "claude-sonnet-4-6", Endpoint: "/v1/messages"},
		providers.TransformPolicy{AuthMode: "payg", Optimizers: map[string]bool{AnthropicStableOptimizerID: true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, applied := applyAnthropicStable(body)
	if !applied || !bytes.Equal(got, want.Body) {
		t.Fatalf("standalone/gateway Anthropic drift:\nstandalone=%s\ngateway=%s", got, want.Body)
	}
}

func TestStandaloneBedrockCompilerMatchesGatewayStableTransform(t *testing.T) {
	body := []byte(`{"toolConfig":{"tools":[{"toolSpec":{"name":"lookup","inputSchema":{"json":{"type":"object"}}}}]},"messages":[{"role":"user","content":[{"text":"question"}]}],"inferenceConfig":{"maxTokens":256}}`)
	model := "global.anthropic.claude-sonnet-4-6"
	want, err := bedrock.New("https://bedrock-runtime.us-east-1.amazonaws.com").ApplyProviderNativeTransforms(
		context.Background(), bytes.NewReader(body),
		providers.RequestMetadata{Provider: "bedrock", Model: model, Region: "us-east-1", Endpoint: "converse"},
		providers.TransformPolicy{AuthMode: "payg", Optimizers: map[string]bool{BedrockCacheOptimizerID: true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, applied := applyBedrockStable(body, "converse")
	var gotObject, wantObject any
	if !applied || json.Unmarshal(got, &gotObject) != nil || json.Unmarshal(want.Body, &wantObject) != nil || !equalJSONValue(gotObject, wantObject) {
		t.Fatalf("standalone/gateway Bedrock drift:\nstandalone=%s\ngateway=%s", got, want.Body)
	}
}

func TestStandaloneBedrockEligibilityMatchesGatewayCatalogPopulation(t *testing.T) {
	models := []string{"missing", "anthropic.claude-legacy", "us.anthropic.claude-sonnet-4-6"}
	for _, entry := range catalog.List() {
		if entry.Provider == "bedrock" {
			models = append(models, entry.Model)
		}
	}
	for _, model := range models {
		if got, want := bedrockCachePointEligibleModel(model), bedrock.CachePointEligibleModel(model); got != want {
			t.Errorf("model %q eligibility=%v gateway=%v", model, got, want)
		}
	}
}

func equalJSONValue(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
