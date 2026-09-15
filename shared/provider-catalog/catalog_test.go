package providercatalog_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

func TestCurrentCatalogPinsDocumentedRoutingEffortLevels(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("catalog", "current.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := catalog.DecodeAndValidate(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"openai:gpt-5.5":              {"none", "low", "medium", "high", "xhigh"},
		"openai:gpt-5.4-mini":         {"none", "low", "medium", "high", "xhigh"},
		"openai:gpt-5.4-nano":         {"none", "low", "medium", "high", "xhigh"},
		"anthropic:claude-sonnet-5":   {"low", "medium", "high", "max", "xhigh"},
		"anthropic:claude-sonnet-4-6": {"low", "medium", "high", "max"},
		"anthropic:claude-opus-5":     {"low", "medium", "high", "max", "xhigh"},
	}
	found := map[string]bool{}
	for _, entry := range entries {
		key := entry.Provider + ":" + entry.Model
		if key == "anthropic:claude-sonnet-4-5" {
			if _, ok := entry.Capabilities["effort_levels"]; ok {
				t.Fatal("Sonnet 4.5 carries unsupported effort capability")
			}
		}
		expected, ok := want[key]
		if !ok {
			continue
		}
		var got []string
		for _, value := range entry.Capabilities["effort_levels"].([]any) {
			got = append(got, value.(string))
		}
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("%s effort_levels = %v, want %v", key, got, expected)
		}
		found[key] = true
	}
	if len(found) != len(want) {
		t.Fatalf("effort-capable rows found = %d, want %d", len(found), len(want))
	}
}

func TestCurrentBedrockCatalogUsesExactModelAndSourceRegionPrices(t *testing.T) {
	tests := []struct {
		model                               string
		wantVersion                         string
		input, output                       float64
		cacheRead, cacheWrite, cacheWrite1h float64
	}{
		// wantVersion is each row's own verified_at (price-provenance) date —
		// an intentional per-model pin, not a single catalog-wide constant.
		// Task C6 split capability provenance into a separate
		// capabilities_verified_at field specifically so capability edits (all
		// 41 rows, same day) don't force every row's *price* date to move
		// together. The 2026-07-30 capability pass nonetheless advanced
		// verified_at on 21 rows, five of them here, and this table's previous
		// comment asserted those five "had their price genuinely re-checked
		// against AWS's pricing page" — an attestation nothing in the diff
		// supports: not one price byte changed, and the commit that moved the
		// dates was a capability commit. The 2026-07-31 review reverted all 21
		// to their real price-provenance dates. capabilities_verified_at stays
		// at 2026-07-30, which is the date that was actually earned.
		{"global.anthropic.claude-opus-4-8", "2026-07-23", 5.00, 25.00, 0.50, 6.25, 10.00},
		{"global.anthropic.claude-sonnet-4-6", "2026-07-23", 3.00, 15.00, 0.30, 3.75, 6.00},
		{"global.anthropic.claude-haiku-4-5-20251001-v1:0", "2026-07-23", 1.00, 5.00, 0.10, 1.25, 2.00},
		{"global.amazon.nova-2-lite-v1:0", "2026-07-23", 0.30, 2.50, 0.075, 0, 0},
		{"us.meta.llama4-maverick-17b-instruct-v1:0", "2026-07-23", 0.24, 0.97, 0, 0, 0},
		{"us.meta.llama4-scout-17b-instruct-v1:0", "2026-07-23", 0.17, 0.66, 0, 0, 0},
		{"mistral.mistral-large-3-675b-instruct", "2026-07-23", 0.50, 1.50, 0, 0, 0},
		{"mistral.ministral-3-8b-instruct", "2026-07-23", 0.15, 0.15, 0, 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			price, version := catalog.PriceForRegion("bedrock", tc.model, "us-east-1")
			if version != tc.wantVersion {
				t.Fatalf("catalog version = %q, want %s", version, tc.wantVersion)
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
	// Every row this test covers (mistral-large-3's 6 non-us-east-1 regions,
	// and all of ministral-3-8b) had capability data filled in by task C6 but
	// its *price* was not independently re-checked against AWS's pricing page,
	// so verified_at (price provenance) stays at its prior date — see the
	// capabilities_verified_at split explained in catalog.go.
	for _, tc := range tests {
		t.Run(tc.model+"@"+tc.region, func(t *testing.T) {
			price, version := catalog.PriceForRegion("bedrock", tc.model, tc.region)
			if version != "2026-07-23" || price.InputPerMillion != tc.input || price.OutputPerMillion != tc.output {
				t.Fatalf("price/version = %+v/%q, want %v/%v/2026-07-23", price, version, tc.input, tc.output)
			}
		})
	}

	for _, entry := range catalog.List() {
		if entry.Provider != "bedrock" {
			continue
		}
		switch entry.Model {
		case "mistral.mistral-large-3-675b-instruct", "mistral.ministral-3-8b-instruct":
			if enabled, _ := entry.Capabilities["responses_api"].(bool); enabled {
				t.Fatalf("%s@%s incorrectly advertises Responses API", entry.Model, entry.Region)
			}
		}
	}
}

// cave-auto's contextFits gate (public/proxy/routing.go) rejects any candidate
// whose catalog capabilities lack a positive context_window_tokens, and
// candidateSupports gates tool/vision/json_mode requests on explicit booleans.
// Before this test, only 3 of the 41 entries carried context_window_tokens at
// all, so the router rejected 38/41 of its own candidate pool.
//
// CW 14: the original version of this test demanded a boolean on every row,
// which is a demand the catalog cannot always honestly meet — and the pass that
// satisfied it filled 20 Bedrock rows with guesses rather than leaving them
// unknown. So what this pins is "an explicit value OR a documented unknown",
// never "always a boolean": the key must be PRESENT (a newly added model that
// forgets it still fails loudly here), and its value must be a bool or an
// explicit null. Null means nobody has checked the vendor's model card; the
// router treats it exactly like absent, so unknown stays fail-closed and can
// never route traffic on a guess.
func TestEveryCatalogEntryHasRoutingCapabilityFields(t *testing.T) {
	entries := catalog.List()
	if len(entries) == 0 {
		t.Fatal("current catalog failed strict runtime validation")
	}
	var incomplete []string
	for _, entry := range entries {
		label := entry.Provider + "/" + entry.Model + "@" + entry.Region
		ctx, ok := entry.Capabilities["context_window_tokens"].(int)
		ctxPositive := ok && ctx > 0
		if !ctxPositive {
			if f, isFloat := entry.Capabilities["context_window_tokens"].(float64); isFloat && f > 0 {
				ctxPositive = true
			}
		}
		if !ctxPositive {
			incomplete = append(incomplete, label+": missing/non-positive context_window_tokens")
		}
		for _, key := range []string{"tools", "vision", "json_mode"} {
			value, present := entry.Capabilities[key]
			if !present {
				incomplete = append(incomplete, label+": capability "+key+" is absent; write an explicit boolean, or an explicit null if the vendor docs do not say")
				continue
			}
			if value == nil {
				continue // documented unknown; the router fails closed on it
			}
			if _, isBool := value.(bool); !isBool {
				incomplete = append(incomplete, label+": capability "+key+" must be a boolean or null")
			}
		}
	}
	if len(incomplete) > 0 {
		t.Fatalf("%d catalog entries fail cave-auto's routing data requirements:\n%s", len(incomplete), strings.Join(incomplete, "\n"))
	}
}

// TestNullCapabilitiesCarryAnUnverifiedNote keeps the narrowing that lets the
// 20 Bedrock rows keep capabilities_verified_at: 2026-07-30 from becoming a
// loophole.
//
// That date attests the capability data a row ASSERTS. An explicit null asserts
// nothing, so it sits outside the date — which is what makes the date honest on
// rows whose tools/vision/json_mode were never checked while their
// context_window_tokens and other keys genuinely were. The whole argument
// depends on a reader being able to see WHICH keys are unattested, so a null
// without a note next to it breaks it: the row would then read as
// "capabilities checked on 2026-07-30" with a silent hole in the middle,
// exactly the shape the verified_at revert removed one field over.
//
// This reads the YAML source rather than the decoded catalog because the note
// is a comment, and a comment nobody checks is decoration.
func TestNullCapabilitiesCarryAnUnverifiedNote(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("catalog", "current.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	var starts []int
	for i, line := range lines {
		if strings.HasPrefix(line, "- provider:") {
			starts = append(starts, i)
		}
	}
	var checked int
	for n, start := range starts {
		end := len(lines)
		if n+1 < len(starts) {
			end = starts[n+1]
		}
		block := strings.Join(lines[start:end], "\n")
		hasNull := false
		for _, key := range []string{"tools", "vision", "json_mode"} {
			if strings.Contains(block, "\n    "+key+": null") {
				hasNull = true
			}
		}
		if !hasNull {
			continue
		}
		checked++
		if !strings.Contains(block, "NOT verified") {
			t.Errorf("row %q carries a null routing capability with no comment naming what was not verified; capabilities_verified_at on that row would read as covering it",
				strings.TrimPrefix(lines[start], "- provider: "))
		}
	}
	if checked == 0 {
		t.Skip("no rows carry a documented-unknown routing capability")
	}
	if checked != 20 {
		t.Errorf("rows with documented-unknown routing capabilities = %d, want 20 (the Bedrock rows the 2026-07-30 pass guessed); if that changed, re-read the capabilities_verified_at narrowing in CLAUDE.md before updating this number", checked)
	}
}
