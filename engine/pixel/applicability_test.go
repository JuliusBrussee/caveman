// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"reflect"
	"testing"
)

func TestAllowedDefaultModels(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "")
	if !reflect.DeepEqual(AllowedModelBases(), []string{"claude-fable-5", "gpt-5.6"}) {
		t.Fatalf("default bases = %#v", AllowedModelBases())
	}
	if !Allowed("us.anthropic.claude-fable-5-v1:0") {
		t.Fatal("Bedrock-prefixed Fable model rejected")
	}
	if !Allowed("claude-fable-5@20260115") {
		t.Fatal("Vertex versioned Fable model rejected")
	}
	if !Allowed("claude-fable-5[1m]") {
		t.Fatal("variant-tagged Fable model rejected")
	}
	if !Allowed("openai/gpt-5.6-preview") {
		t.Fatal("provider-prefixed GPT model rejected")
	}
	if Allowed("claude-opus-4-8") {
		t.Fatal("Opus allowed by default")
	}
	if Allowed("claude-opus-4-8@20260101") {
		t.Fatal("Vertex versioned Opus allowed by default")
	}
}

func TestAllowedModelEnv(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "off")
	if Allowed("claude-fable-5") || len(AllowedModelBases()) != 0 {
		t.Fatal("off env did not disable models")
	}
	t.Setenv("CAVE_PIXEL_MODELS", "gemini-3, claude-opus-4-8")
	if !Allowed("projects/x/locations/y/publishers/google/models/gemini-3-pro") {
		t.Fatal("custom Gemini model rejected")
	}
	if !Allowed("claude-opus-4-8") {
		t.Fatal("custom Opus model rejected")
	}
	if Allowed("gpt-5.6") {
		t.Fatal("default GPT allowed despite custom env")
	}
}
