package providercatalog_test

import (
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

func TestCurrentBedrockCatalogUsesExactModelAndSourceRegionPrices(t *testing.T) {
	tests := []struct {
		model                               string
		input, output                       float64
		cacheRead, cacheWrite, cacheWrite1h float64
	}{
		{"global.anthropic.claude-opus-4-8", 5.00, 25.00, 0.50, 6.25, 10.00},
		{"global.anthropic.claude-sonnet-4-6", 3.00, 15.00, 0.30, 3.75, 6.00},
		{"global.anthropic.claude-haiku-4-5-20251001-v1:0", 1.00, 5.00, 0.10, 1.25, 2.00},
		{"anthropic.claude-sonnet-4-6-v1", 3.30, 16.50, 0.33, 4.125, 6.60},
		{"global.amazon.nova-2-lite-v1:0", 0.30, 2.50, 0.075, 0, 0},
		{"us.meta.llama4-maverick-17b-instruct-v1:0", 0.24, 0.97, 0, 0, 0},
		{"us.meta.llama4-scout-17b-instruct-v1:0", 0.17, 0.66, 0, 0, 0},
		{"mistral.mistral-large-3-675b-instruct", 0.50, 1.50, 0, 0, 0},
		{"mistral.ministral-3-8b-instruct", 0.15, 0.15, 0, 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			price, version := catalog.PriceForRegion("bedrock", tc.model, "us-east-1")
			if version != "2026-07-23" {
				t.Fatalf("catalog version = %q, want 2026-07-23", version)
			}
			if price.InputPerMillion != tc.input || price.OutputPerMillion != tc.output {
				t.Fatalf("price = %+v, want input=%v output=%v", price, tc.input, tc.output)
			}
			if price.CacheReadPerMillion != tc.cacheRead ||
				price.CacheWritePerMillion != tc.cacheWrite ||
				price.CacheWrite1hPerMillion != tc.cacheWrite1h {
				t.Fatalf(
					"cache price = %v/%v/%v, want read=%v write5m=%v write1h=%v",
					price.CacheReadPerMillion,
					price.CacheWritePerMillion,
					price.CacheWrite1hPerMillion,
					tc.cacheRead,
					tc.cacheWrite,
					tc.cacheWrite1h,
				)
			}
		})
	}
}

func TestBedrockCatalogDoesNotBorrowSourceRegionOrInventedModelPrices(t *testing.T) {
	for _, tc := range []struct {
		model, region string
	}{
		{"global.amazon.nova-2-lite-v1:0", "eu-west-1"},
		{"global.anthropic.claude-opus-4-8", "global"},
		{"us.meta.llama4-maverick-17b-instruct-v1:1", "us-east-1"},
	} {
		price, version := catalog.PriceForRegion("bedrock", tc.model, tc.region)
		if price != (cost.Price{}) {
			t.Errorf("%s@%s price = %+v, want honest zero", tc.model, tc.region, price)
		}
		if !strings.HasPrefix(version, "unpriced:") {
			t.Errorf("%s@%s version = %q, want unpriced prefix", tc.model, tc.region, version)
		}
	}
}

func TestCurrentMistralRegionalPricingAndCapabilities(t *testing.T) {
	tests := []struct {
		model, region string
		input, output float64
	}{
		{"mistral.mistral-large-3-675b-instruct", "us-east-2", 0.50, 1.50},
		{"mistral.mistral-large-3-675b-instruct", "us-west-2", 0.50, 1.50},
		{"mistral.mistral-large-3-675b-instruct", "ap-south-1", 0.59, 1.76},
		{"mistral.mistral-large-3-675b-instruct", "ap-northeast-1", 0.61, 1.82},
		{"mistral.mistral-large-3-675b-instruct", "sa-east-1", 0.61, 1.82},
		{"mistral.mistral-large-3-675b-instruct", "ap-southeast-2", 0.515, 1.545},
		{"mistral.ministral-3-8b-instruct", "us-east-2", 0.15, 0.15},
		{"mistral.ministral-3-8b-instruct", "us-west-2", 0.15, 0.15},
		{"mistral.ministral-3-8b-instruct", "ap-south-1", 0.18, 0.18},
		{"mistral.ministral-3-8b-instruct", "ap-northeast-1", 0.18, 0.18},
		{"mistral.ministral-3-8b-instruct", "sa-east-1", 0.18, 0.18},
		{"mistral.ministral-3-8b-instruct", "eu-west-1", 0.18, 0.18},
		{"mistral.ministral-3-8b-instruct", "eu-south-1", 0.18, 0.18},
		{"mistral.ministral-3-8b-instruct", "eu-west-2", 0.23, 0.23},
		{"mistral.ministral-3-8b-instruct", "ap-southeast-2", 0.1545, 0.1545},
	}
	for _, tc := range tests {
		t.Run(tc.model+"@"+tc.region, func(t *testing.T) {
			price, version := catalog.PriceForRegion("bedrock", tc.model, tc.region)
			if version != "2026-07-23" || price.InputPerMillion != tc.input || price.OutputPerMillion != tc.output {
				t.Fatalf("price/version = %+v/%q, want %v/%v/2026-07-23", price, version, tc.input, tc.output)
			}
		})
	}

	for _, entry := range catalog.List() {
		if entry.Provider == "bedrock" && entry.Model == "mistral.mistral-large-3-675b-instruct" {
			if enabled, _ := entry.Capabilities["responses_api"].(bool); enabled {
				t.Fatalf("Mistral Large 3@%s incorrectly advertises Responses API", entry.Region)
			}
		}
	}
}
