package gateway

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// Vertex retired gemini-2.5-pro on 2026-10-20 and its row left the catalog
	// on 2026-11-06. Its traffic must price to an honest zero. It must not
	// borrow the Gemini API row that still prices the same model id. The
	// region-agnostic rule this case used to exercise is now proved on a
	// fixture in TestStandaloneVertexRegionAgnosticPricingOnFixtureCatalog.
	if retired := standalonePriceForUsage(providers.RequestMetadata{Provider: "vertex", Model: "gemini-2.5-pro", Region: "us-central1"}, usage); retired != (cost.Price{}) {
		t.Fatalf("retired Vertex gemini-2.5-pro still priced: %+v", retired)
	}
	if apiPrice, _ := catalog.Price("gemini", "gemini-2.5-pro"); apiPrice.InputPerMillion == 0 {
		t.Fatal("Gemini API gemini-2.5-pro must stay priced, or the borrow check above proves nothing")
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

// standaloneRegionAgnosticProbeEnv switches the fixture probe below on. The
// probe needs its own process because the catalog package loads its catalog
// once per process, from the embedded file or CAVE_CATALOG_PATH.
const standaloneRegionAgnosticProbeEnv = "CAVEMAN_GATEWAY_REGION_AGNOSTIC_PROBE"

// The fixture prices are invented; they describe no vendor's real list.
const standaloneRegionAgnosticFixture = `
- provider: vertex
  model: fixture-agnostic
  region: global
  currency: USD
  pricing:
    input_per_million: 1.25
    output_per_million: 10
    cache_read_input_per_million: null
    cache_write_input_per_million: null
    cache_write_1h_input_per_million: null
    reasoning_output_per_million: null
    batch_discount_fraction: null
    cache_storage_per_million_tokens_hour: null
  capabilities:
    region_agnostic_pricing: true
  sources: [https://example.com/pricing]
  verified_at: 2026-11-06T00:00:00Z
- provider: vertex
  model: fixture-no-flag
  region: global
  currency: USD
  pricing:
    input_per_million: 3
    output_per_million: 15
    cache_read_input_per_million: null
    cache_write_input_per_million: null
    cache_write_1h_input_per_million: null
    reasoning_output_per_million: null
    batch_discount_fraction: null
    cache_storage_per_million_tokens_hour: null
  capabilities: {}
  sources: [https://example.com/pricing]
  verified_at: 2026-11-06T00:00:00Z
`

// TestStandaloneVertexRegionAgnosticPricingOnFixtureCatalog proves that a
// regional Vertex request takes a global row's price only when that row
// declares region_agnostic_pricing. Until 2026-11-06 the real Vertex Gemini 2.5
// rows covered this. Vertex retired both models, so no current row carries the
// flag, and this fixture keeps the rule tested.
func TestStandaloneVertexRegionAgnosticPricingOnFixtureCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.yaml")
	if err := os.WriteFile(path, []byte(standaloneRegionAgnosticFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStandaloneVertexRegionAgnosticFixtureProbe$", "-test.v")
	cmd.Env = append(os.Environ(), standaloneRegionAgnosticProbeEnv+"=1", "CAVE_CATALOG_PATH="+path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture probe failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "fixture probe ran on 2 rows") || !strings.Contains(string(out), "--- PASS: TestStandaloneVertexRegionAgnosticFixtureProbe") {
		t.Fatalf("fixture probe did not run on the fixture catalog:\n%s", out)
	}
}

// TestStandaloneVertexRegionAgnosticFixtureProbe does nothing in a normal run.
// It is the child process of the test above.
func TestStandaloneVertexRegionAgnosticFixtureProbe(t *testing.T) {
	if os.Getenv(standaloneRegionAgnosticProbeEnv) != "1" {
		return
	}
	if rows := len(catalog.List()); rows != 2 {
		t.Fatalf("probe loaded %d rows, want the 2 fixture rows (CAVE_CATALOG_PATH ignored?)", rows)
	}
	t.Log("fixture probe ran on 2 rows")
	usage := completeUsageForPricing()
	agnostic := standalonePriceForUsage(providers.RequestMetadata{Provider: "vertex", Model: "fixture-agnostic", Region: "us-central1"}, usage)
	if agnostic.InputPerMillion != 1.25 || agnostic.OutputPerMillion != 10 {
		t.Errorf("region-agnostic Vertex row did not price a us-central1 request: %+v", agnostic)
	}
	if regional := standalonePriceForUsage(providers.RequestMetadata{Provider: "vertex", Model: "fixture-no-flag", Region: "us-central1"}, usage); regional != (cost.Price{}) {
		t.Errorf("a global Vertex row without the flag priced a us-central1 request: %+v", regional)
	}
	if global := standalonePriceForUsage(providers.RequestMetadata{Provider: "vertex", Model: "fixture-no-flag", Region: "global"}, usage); global.InputPerMillion != 3 {
		t.Errorf("a global Vertex request lost its own global price: %+v", global)
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
