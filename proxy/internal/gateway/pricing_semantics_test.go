package gateway

import (
	"math"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

func completeUsageForPricing() providers.UsageObservation {
	return providers.UsageObservation{InputTokensReported: true, OutputTokensReported: true}
}

func TestStandalonePricingMatchesManagedRegionalRules(t *testing.T) {
	usage := completeUsageForPricing()
	vertex := standalonePriceForUsage(providers.RequestMetadata{Provider: "vertex", Model: "gemini-2.5-pro", Region: "us-central1"}, usage)
	if vertex.InputPerMillion == 0 {
		t.Fatal("Vertex Gemini should use its explicitly region-agnostic global list price")
	}
	vertexClaude := standalonePriceForUsage(providers.RequestMetadata{Provider: "vertex", Model: "claude-sonnet-4-6", Region: "us-central1"}, usage)
	if vertexClaude.InputPerMillion != 0 {
		t.Fatal("Vertex Claude must not borrow a global marketplace price for an unknown region")
	}

	base, _ := catalog.Price("openai", "gpt-5.5")
	regional := standalonePriceForUsage(providers.RequestMetadata{Provider: "openai", Model: "gpt-5.5", Region: "eu"}, usage)
	if math.Abs(regional.InputPerMillion-base.InputPerMillion*1.10) > 1e-12 {
		t.Fatalf("OpenAI regional input price = %v, want model-attested 10%% uplift", regional.InputPerMillion)
	}
	embedding := standalonePriceForUsage(providers.RequestMetadata{Provider: "openai", Model: "text-embedding-3-small", Region: "eu"}, usage)
	if embedding.InputPerMillion != 0 {
		t.Fatal("models without a regional-processing capability must remain unpriced")
	}

	baseAnthropic, _ := catalog.Price("anthropic", "claude-sonnet-4-6")
	usage.InferenceGeo = "us"
	usAnthropic := standalonePriceForUsage(providers.RequestMetadata{Provider: "anthropic", Model: "claude-sonnet-4-6"}, usage)
	if math.Abs(usAnthropic.InputPerMillion-baseAnthropic.InputPerMillion*1.10) > 1e-12 {
		t.Fatalf("Anthropic US inference price = %v, want model-attested 10%% uplift", usAnthropic.InputPerMillion)
	}
}

func TestOpenAIPromptCacheKeyCannotClaimCausalSavings(t *testing.T) {
	if hasCacheOptimizer([]string{"openai-prompt-cache-key"}) {
		t.Fatal("OpenAI cache affinity is not proof the hint caused an automatic cache hit")
	}
	if !hasCacheOptimizer([]string{"anthropic-cache-breakpoints"}) {
		t.Fatal("the Anthropic breakpoint transform remains the supported causal cache optimizer")
	}
}

func TestAnthropicAutomaticPromptCacheCannotClaimCausalSavings(t *testing.T) {
	if hasCacheOptimizer([]string{anthropic.AutomaticPromptCacheOptimizerID}) {
		t.Fatal("Anthropic automatic placement must remain measured-only, never a causal cache-savings optimizer")
	}
}

func TestStandaloneCostBreakdownUsesDistinctReasoningRate(t *testing.T) {
	usage := completeUsageForPricing()
	usage.OutputTokens = 100
	usage.ReasoningTokens = 40
	_, output, _ := costBreakdown("gemini", cost.Price{OutputPerMillion: 10, ReasoningPerMillion: 25}, usage)
	want := float64(60*10+40*25) / 1_000_000
	if math.Abs(output-want) > 1e-12 {
		t.Fatalf("reasoning-aware output cost = %.10f, want %.10f", output, want)
	}
}
