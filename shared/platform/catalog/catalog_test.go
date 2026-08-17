package catalog_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers/bedrock"
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

// Every model the e2e demo / smoke test / examples exercise must resolve to a
// real priced entry, otherwise cost reporting silently reads zero.
func TestPriceCoversExercisedModels(t *testing.T) {
	models := []struct{ provider, model string }{
		{"openai", "gpt-5.5"},
		{"openai", "gpt-5.4-mini"},
		{"openai", "gpt-5.4-nano"},
		{"anthropic", "claude-sonnet-4-6"},
		{"gemini", "gemini-2.5-pro"},
	}
	for _, m := range models {
		price, version := catalog.Price(m.provider, m.model)
		if price.InputPerMillion <= 0 {
			t.Errorf("%s/%s input price = %v, want > 0", m.provider, m.model, price.InputPerMillion)
		}
		if price.OutputPerMillion <= 0 {
			t.Errorf("%s/%s output price = %v, want > 0", m.provider, m.model, price.OutputPerMillion)
		}
		if version == "" || strings.HasPrefix(version, "unpriced") {
			t.Errorf("%s/%s version = %q, want a real catalog date", m.provider, m.model, version)
		}
	}
}

func TestCurrentCatalogRowsAreFreshAndStructurallyValid(t *testing.T) {
	entries := catalog.List()
	if len(entries) == 0 {
		t.Fatal("current catalog failed strict runtime validation")
	}
	now := time.Now().UTC()
	for _, entry := range entries {
		verified, err := time.Parse(time.RFC3339, entry.VerifiedAt)
		if err != nil {
			t.Fatalf("%s/%s verified_at = %q: %v", entry.Provider, entry.Model, entry.VerifiedAt, err)
		}
		if verified.After(now.Add(24 * time.Hour)) {
			t.Errorf("%s/%s verified_at %s is in the future", entry.Provider, entry.Model, verified)
		}
		if now.Sub(verified) > 120*24*time.Hour {
			t.Errorf("%s/%s price verification is stale (%s; max 120 days)", entry.Provider, entry.Model, verified.Format("2006-01-02"))
		}
	}
}

// TestOldestVerifiedAtIsNotSilentlyAdvanced pins the catalog's oldest price
// verification date to a known value. VerifiedAt must move only when a row's
// PRICE is re-checked against the vendor — never as a side effect of editing
// capabilities, sources, or anything else on that row. If this test breaks
// because a date moved without a matching pricing-page recheck (and this
// literal updated to match), that is exactly the conflation bug task C6's
// review caught: a capability-only edit silently re-attesting a price nobody
// looked at, resetting the 120-day staleness alarm on a row that needed it
// most. anthropic.claude-3-5-haiku-20241022-v1:0 is deliberately the pinned
// row: it carries the oldest verified_at in the catalog today, so it is the
// one closest to actually firing TestCurrentCatalogRowsAreFreshAndStructurallyValid's
// staleness alarm — the exact row a silent bump would put back to sleep.
func TestOldestVerifiedAtIsNotSilentlyAdvanced(t *testing.T) {
	entries := catalog.List()
	if len(entries) == 0 {
		t.Fatal("current catalog failed strict runtime validation")
	}
	var oldest catalog.Entry
	var oldestTime time.Time
	for i, entry := range entries {
		verified, err := time.Parse(time.RFC3339, entry.VerifiedAt)
		if err != nil {
			t.Fatalf("%s/%s verified_at = %q: %v", entry.Provider, entry.Model, entry.VerifiedAt, err)
		}
		if i == 0 || verified.Before(oldestTime) {
			oldest, oldestTime = entry, verified
		}
	}
	wantProvider, wantModel, wantVerifiedAt := "bedrock", "anthropic.claude-3-5-haiku-20241022-v1:0", "2026-06-14T00:00:00Z"
	if oldest.Provider != wantProvider || oldest.Model != wantModel || oldest.VerifiedAt != wantVerifiedAt {
		t.Fatalf("oldest verified_at row = %s/%s@%s verified_at=%s, want %s/%s verified_at=%s (a capability-only edit must never move this)",
			oldest.Provider, oldest.Model, oldest.Region, oldest.VerifiedAt, wantProvider, wantModel, wantVerifiedAt)
	}

	// Pinning only the OLDEST row left the mass re-attest undetectable: on
	// 2026-07-30 a capability-data pass advanced verified_at on 21 rows whose
	// prices it never re-checked, and because the oldest row (2026-06-14) was
	// not among them, this test stayed green while 21 staleness clocks were
	// silently reset. So pin the whole distribution, not its minimum. A real
	// price recheck updates this map in the same commit as the price; nothing
	// else may. Any explicitly price-verified new row (such as Claude Sonnet 5)
	// must likewise update this expectation in the same commit as its catalog
	// price row; capability-only changes must not alter VerifiedAt.
	wantDates := map[string]int{
		"2026-06-14T00:00:00Z": 1,
		"2026-07-10T00:00:00Z": 15,
		"2026-07-23T00:00:00Z": 23,
		"2026-08-05T00:00:00Z": 1,
		"2026-08-07T00:00:00Z": 1,
		"2026-08-10T00:00:00Z": 9,
	}
	gotDates := map[string]int{}
	for _, entry := range entries {
		gotDates[entry.VerifiedAt]++
	}
	if !reflect.DeepEqual(gotDates, wantDates) {
		t.Errorf("verified_at distribution = %v, want %v (a verified_at moves ONLY with the price on that row; if this fired on a capability edit, revert the date)", gotDates, wantDates)
	}
}

// TestEveryCatalogEntryHasCapabilitiesVerifiedAt guards the inverse mistake
// from the one TestOldestVerifiedAtIsNotSilentlyAdvanced guards: forgetting to
// record capability provenance at all, rather than wrongly re-dating pricing
// provenance to cover for it. Every entry in this catalog has had its
// capability data (tools/vision/json_mode/context_window_tokens) checked
// against the vendor's own model docs, so every entry must carry a
// capabilities_verified_at distinct from — and not read as — VerifiedAt.
func TestEveryCatalogEntryHasCapabilitiesVerifiedAt(t *testing.T) {
	entries := catalog.List()
	if len(entries) == 0 {
		t.Fatal("current catalog failed strict runtime validation")
	}
	for _, entry := range entries {
		if entry.CapabilitiesVerifiedAt == "" {
			t.Errorf("%s/%s@%s missing capabilities_verified_at", entry.Provider, entry.Model, entry.Region)
			continue
		}
		verified, err := time.Parse(time.RFC3339, entry.CapabilitiesVerifiedAt)
		if err != nil {
			t.Errorf("%s/%s@%s capabilities_verified_at = %q: %v", entry.Provider, entry.Model, entry.Region, entry.CapabilitiesVerifiedAt, err)
			continue
		}
		if verified.After(time.Now().UTC().Add(24 * time.Hour)) {
			t.Errorf("%s/%s@%s capabilities_verified_at %s is in the future", entry.Provider, entry.Model, entry.Region, verified)
		}
	}
}

// pricingIdentity is the slice of an Entry that the immutable dated snapshot
// pins: the pricing columns plus every key in
// catalog.PriceAffectingCapabilities. That set is what a signed receipt's
// catalog_version can honestly be said to attest to — and it is only true as
// long as the registry stays complete, which PricingMultiplier's fail-closed
// check and TestPricingMultiplierRefusesUnregisteredCapabilities enforce.
// CapabilitiesVerifiedAt and every capability EXCEPT the price multipliers named
// by pricingMultiplierKeys are deliberately excluded — capability
// data (tools/vision/json_mode/context_window_tokens) is free to be filled in
// or corrected on a row without minting a new price-dated snapshot, precisely
// because doing so must NOT require moving VerifiedAt (see catalog.go's
// comment on that field). Sources is excluded too: the schema keeps one shared
// citation list for both pricing and capability sources (splitting that is a
// bigger schema change than this comparison should force), so a row can gain a
// capability citation without its price snapshot going stale. Pricing changes
// remain fully protected: the Pricing struct itself is still pinned exactly,
// and so is every capability the gateway multiplies a price by (see
// TestTamperedPriceMultiplierBreaksTheSnapshotPin for what that exclusion cost
// before this field existed).
// Sources isn't fully unguarded despite being out of this struct: see the
// subset check in TestEveryCurrentCatalogRowMatchesItsImmutableDatedSnapshot,
// which still forbids a PRICING citation from being swapped or dropped while
// verified_at stays put — only additions (typically capability citations)
// pass silently.
type pricingIdentity struct {
	Provider    string
	Model       string
	Region      string
	Currency    string
	Pricing     cost.Price
	VerifiedAt  string
	Multipliers map[string]any
}

func pricingIdentityOf(e catalog.Entry) pricingIdentity {
	// catalog.PriceAffectingCapabilities is read directly, never copied: the
	// exact failure this guards is one list being updated and another not.
	// Present-with-a-value and absent are different attestations, so record only
	// the keys the row actually carries — adding a price-affecting capability to
	// a row that had none changes that row's price and must break the pin
	// exactly like editing one that was already there.
	multipliers := map[string]any{}
	for _, key := range catalog.PriceAffectingCapabilities {
		if value, ok := e.Capabilities[key]; ok {
			multipliers[key] = value
		}
	}
	return pricingIdentity{
		Provider:    e.Provider,
		Model:       e.Model,
		Region:      e.Region,
		Currency:    e.Currency,
		Pricing:     e.Pricing,
		VerifiedAt:  e.VerifiedAt,
		Multipliers: multipliers,
	}
}

// isSubset reports whether every element of sub also appears in super.
func isSubset(sub, super []string) bool {
	set := make(map[string]struct{}, len(super))
	for _, s := range super {
		set[s] = struct{}{}
	}
	for _, s := range sub {
		if _, ok := set[s]; !ok {
			return false
		}
	}
	return true
}

const catalogDir = "../../provider-catalog/catalog"

type snapshotKey struct{ provider, model, region string }

// loadCurrentCatalog decodes current.yaml through the same strict validator the
// runtime uses, so a probe test mutates exactly the rows the gateway would load.
func loadCurrentCatalog(t *testing.T) []catalog.Entry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(catalogDir, "current.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := catalog.DecodeAndValidate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// snapshotDrift runs the immutable-dated-snapshot pin over a decoded catalog and
// returns one message per row that drifted from the snapshot its own verified_at
// names. It is a function rather than inline test body so a probe can feed it a
// deliberately tampered catalog and assert the pin actually catches the tamper —
// a pin is only worth what its miss looks like.
func snapshotDrift(t *testing.T, current []catalog.Entry) []string {
	t.Helper()
	var drift []string
	snapshots := map[string]map[snapshotKey]catalog.Entry{}
	for _, entry := range current {
		verified, _ := time.Parse(time.RFC3339, entry.VerifiedAt)
		version := verified.Format("2006-01-02")
		rows, loaded := snapshots[version]
		if !loaded {
			raw, readErr := os.ReadFile(filepath.Join(catalogDir, version+".yaml"))
			if readErr != nil {
				t.Fatalf("%s/%s@%s references missing immutable snapshot %s: %v", entry.Provider, entry.Model, entry.Region, version, readErr)
			}
			decoded, decodeErr := catalog.DecodeAndValidate(raw)
			if decodeErr != nil {
				t.Fatalf("snapshot %s is invalid: %v", version, decodeErr)
			}
			rows = make(map[snapshotKey]catalog.Entry, len(decoded))
			for _, row := range decoded {
				rows[snapshotKey{row.Provider, row.Model, row.Region}] = row
			}
			snapshots[version] = rows
		}
		got, ok := rows[snapshotKey{entry.Provider, entry.Model, entry.Region}]
		if !ok {
			drift = append(drift, fmt.Sprintf("%s/%s@%s missing from immutable snapshot %s", entry.Provider, entry.Model, entry.Region, version))
			continue
		}
		if !reflect.DeepEqual(pricingIdentityOf(got), pricingIdentityOf(entry)) {
			drift = append(drift, fmt.Sprintf("%s/%s@%s pricing changed without a new verified_at snapshot version", entry.Provider, entry.Model, entry.Region))
		}
		// Sources is excluded from pricingIdentity so a capability citation can
		// be added without minting a new price-dated snapshot, but a PRICING
		// citation must never be silently swapped or dropped while verified_at
		// stays put — that would let a price change hide behind an unmoved
		// snapshot date. The snapshot's sources must remain a subset of the
		// current row's; only additions are allowed.
		if !isSubset(got.Sources, entry.Sources) {
			drift = append(drift, fmt.Sprintf("%s/%s@%s dropped or replaced a source from its immutable snapshot %s without a new verified_at (snapshot had %v, current has %v)", entry.Provider, entry.Model, entry.Region, version, got.Sources, entry.Sources))
		}
	}
	return drift
}

func TestEveryCurrentCatalogRowMatchesItsImmutableDatedSnapshot(t *testing.T) {
	for _, message := range snapshotDrift(t, loadCurrentCatalog(t)) {
		t.Error(message)
	}
}

// TestTamperedPriceCapabilityBreaksTheSnapshotPin reproduces the 2026-07-31
// review probe verbatim: change regional_processing_multiplier from the 1.10 the
// catalog cites to 1.95 and every OpenAI us/eu token rate the gateway prices
// with inflates by 77%, because proxy.go multiplies the row's rates by it
// (scaleStandaloneTokenRates). Before these keys were folded into
// pricingIdentity that edit passed EVERY gate — the snapshot pin (they live in
// capabilities, which the pin deliberately excludes), the freshness check, the
// JSON schema, and validate_catalog.py — so a signed receipt would have
// attested a catalog_version whose money no longer matched the snapshot that
// version names. A capability may be excluded from the price pin; a capability
// that decides a price may not.
//
// It runs over EVERY key in catalog.PriceAffectingCapabilities, not a list of
// its own, so a key registered there without being pinned fails here. That
// caught region_agnostic_pricing, whose blast radius is larger than the
// multipliers': removing it drops a whole Vertex region's spend to an honest
// zero while verified_at, and therefore the receipt, says nothing changed.
func TestTamperedPriceCapabilityBreaksTheSnapshotPin(t *testing.T) {
	for _, capability := range catalog.PriceAffectingCapabilities {
		t.Run(capability, func(t *testing.T) {
			current := loadCurrentCatalog(t)
			if drift := snapshotDrift(t, current); len(drift) != 0 {
				t.Fatalf("catalog already drifts before tampering: %v", drift)
			}
			tampered := false
			for i := range current {
				// Tamper by type: a multiplier moves to the reviewer's exact 1.95
				// probe value (against a cited 1.10), a boolean flips.
				var probe any
				switch original := current[i].Capabilities[capability].(type) {
				case float64:
					if original == 1.95 {
						t.Fatalf("%s: probe value equals the shipped value", capability)
					}
					probe = 1.95
				case bool:
					probe = !original
				default:
					continue
				}
				caps := make(map[string]any, len(current[i].Capabilities))
				for k, v := range current[i].Capabilities {
					caps[k] = v
				}
				caps[capability] = probe
				current[i].Capabilities = caps
				tampered = true
				break
			}
			if !tampered {
				t.Fatalf("no catalog row carries %s — the probe would prove nothing", capability)
			}
			if drift := snapshotDrift(t, current); len(drift) == 0 {
				t.Fatalf("a tampered %s passed the immutable snapshot pin: a price-affecting capability escaped the version a signed receipt attests", capability)
			}
		})

		// DELETION is the other half, and for region_agnostic_pricing it is the
		// worse half: removing that one line drops every non-global Vertex
		// request to an honest-zero `unpriced:`, taking that traffic's measured
		// spend to $0 while verified_at and the receipt's catalog_version say
		// nothing changed. Editing a value and removing it entirely must both
		// break the pin.
		t.Run(capability+"/deleted", func(t *testing.T) {
			current := loadCurrentCatalog(t)
			deleted := false
			for i := range current {
				if _, ok := current[i].Capabilities[capability]; !ok {
					continue
				}
				caps := make(map[string]any, len(current[i].Capabilities))
				for k, v := range current[i].Capabilities {
					caps[k] = v
				}
				delete(caps, capability)
				current[i].Capabilities = caps
				deleted = true
				break
			}
			if !deleted {
				t.Fatalf("no catalog row carries %s — the probe would prove nothing", capability)
			}
			if drift := snapshotDrift(t, current); len(drift) == 0 {
				t.Fatalf("DELETING %s passed the immutable snapshot pin: a price-affecting capability can be removed without the version a signed receipt attests changing", capability)
			}
		})
	}
}

// PricingMultiplier fails closed on any capability not registered as
// price-affecting. That refusal is what makes the registry enforcement rather
// than documentation: a new multiplier either appears in
// catalog.PriceAffectingCapabilities — and is therefore pinned by the snapshot
// above — or it does not multiply anything. The alternative, a comment asking
// the next author to update a second list, is exactly what let
// region_agnostic_pricing escape the pin after the multipliers were fixed.
func TestPricingMultiplierRefusesUnregisteredCapabilities(t *testing.T) {
	if _, ok := catalog.PricingMultiplier("openai", "gpt-5.5", "global", "regional_processing_multiplier"); !ok {
		t.Fatal("a registered multiplier was refused")
	}
	for _, capability := range []string{"batch_discount_fraction", "context_window_tokens", "made_up_multiplier"} {
		if value, ok := catalog.PricingMultiplier("openai", "gpt-5.5", "global", capability); ok {
			t.Errorf("unregistered capability %q applied as a price multiplier (=%v); it would move money the snapshot does not pin", capability, value)
		}
	}
}

func TestDecodeAndValidateRejectsUnsafeCatalogs(t *testing.T) {
	valid := `
- provider: openai
  model: gpt-test
  region: global
  currency: USD
  pricing:
    input_per_million: 1
    output_per_million: 2
    cache_read_input_per_million: null
    cache_write_input_per_million: null
    cache_write_1h_input_per_million: null
    reasoning_output_per_million: null
    batch_discount_fraction: 0.5
    cache_storage_per_million_tokens_hour: null
  capabilities: {}
  sources: [https://example.com/pricing]
  verified_at: 2026-07-10T00:00:00Z
`
	if _, err := catalog.DecodeAndValidate([]byte(valid)); err != nil {
		t.Fatalf("valid catalog rejected: %v", err)
	}
	tests := []struct {
		name, mutate string
	}{
		{"unknown pricing field", strings.Replace(valid, "input_per_million: 1", "input_per_million_typo: 1", 1)},
		{"negative rate", strings.Replace(valid, "input_per_million: 1", "input_per_million: -1", 1)},
		{"non https source", strings.Replace(valid, "https://example.com/pricing", "http://example.com/pricing", 1)},
		{"invalid timestamp", strings.Replace(valid, "2026-07-10T00:00:00Z", "yesterday", 1)},
		{"duplicate row", valid + valid},
		{"empty", "[]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := catalog.DecodeAndValidate([]byte(tc.mutate)); err == nil {
				t.Fatal("unsafe catalog accepted")
			}
		})
	}
}

// An unknown model must not silently borrow another model's price; it returns a
// zero price flagged as unpriced so the cost is honestly zero, not wrong.
func TestPriceUnknownModelIsHonestZero(t *testing.T) {
	price, version := catalog.Price("openai", "model-that-does-not-exist")
	if price.InputPerMillion != 0 || price.OutputPerMillion != 0 {
		t.Errorf("unknown model priced %+v, want zero", price)
	}
	if !strings.HasPrefix(version, "unpriced:") {
		t.Errorf("version = %q, want unpriced: prefix", version)
	}
}

func TestOfficialRuntimeModelPricesAndTiers(t *testing.T) {
	tests := []struct {
		provider, model                 string
		input, cached, output           float64
		threshold                       int
		longInputMultiplier, longOutput float64
	}{
		{"openai", "gpt-5.5", 5, .5, 30, 272_000, 2, 1.5},
		{"openai", "gpt-5.4-mini", .75, .075, 4.5, 0, 0, 0},
		{"openai", "gpt-5.4-nano", .20, .02, 1.25, 0, 0, 0},
		{"anthropic", "claude-sonnet-4-6", 3, .3, 15, 0, 0, 0},
		{"gemini", "gemini-2.5-pro", 1.25, .125, 10, 200_000, 2, 1.5},
		{"vertex", "claude-sonnet-4-6", 3, .3, 15, 0, 0, 0},
		{"bedrock", "anthropic.claude-3-5-sonnet-20241022-v2:0", 6, .6, 30, 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			price, version := catalog.Price(tc.provider, tc.model)
			if tc.provider == "bedrock" {
				price, version = catalog.PriceForRegion(tc.provider, tc.model, "us-east-1")
			}
			if strings.HasPrefix(version, "unpriced:") {
				t.Fatalf("model unpriced: %s", version)
			}
			if price.InputPerMillion != tc.input || price.CacheReadPerMillion != tc.cached || price.OutputPerMillion != tc.output {
				t.Fatalf("price = %+v, want input=%v cached=%v output=%v", price, tc.input, tc.cached, tc.output)
			}
			if price.LongContextThresholdTokens != tc.threshold || price.LongContextInputMultiplier != tc.longInputMultiplier || price.LongContextOutputMultiplier != tc.longOutput {
				t.Fatalf("long-context tier = %+v, want threshold=%d multipliers=%v/%v", price, tc.threshold, tc.longInputMultiplier, tc.longOutput)
			}
		})
	}
	for _, invented := range []string{"gpt-5.5-mini", "gpt-5.5-nano"} {
		if _, version := catalog.Price("openai", invented); !strings.HasPrefix(version, "unpriced:") {
			t.Errorf("invented model %q remains priced: %q", invented, version)
		}
	}
	if price, version := catalog.Price("bedrock", "anthropic.claude-3-5-sonnet-20241022-v2:0"); price != (cost.Price{}) || !strings.HasPrefix(version, "unpriced:") {
		t.Fatalf("regionless Bedrock lookup borrowed a regional price: %+v %q", price, version)
	}
	if price, version := catalog.PriceForRegionOrAgnostic("vertex", "gemini-2.5-pro", "us-central1"); price.InputPerMillion != 1.25 || strings.HasPrefix(version, "unpriced:") {
		t.Fatalf("region-agnostic Vertex price unavailable: %+v %q", price, version)
	}
	if price, version := catalog.PriceForRegionOrAgnostic("vertex", "claude-sonnet-4-6", "us-central1"); price != (cost.Price{}) || !strings.HasPrefix(version, "unpriced:") {
		t.Fatalf("regional Claude price borrowed global row: %+v %q", price, version)
	}
	if multiplier, ok := catalog.PricingMultiplier("openai", "gpt-5.5", "global", "regional_processing_multiplier"); !ok || multiplier != 1.1 {
		t.Fatalf("OpenAI regional multiplier = %v/%v, want 1.1/true", multiplier, ok)
	}
	if _, ok := catalog.PricingMultiplier("openai", "text-embedding-3-small", "global", "regional_processing_multiplier"); ok {
		t.Fatal("embedding model inherited an undocumented regional multiplier")
	}
	// The Anthropic us-geo premium is the second multiplier the gateway applies
	// to a catalog price (proxy.go: geo == "us" → scaleStandaloneTokenRates). It
	// went unpinned at the consumption site while the OpenAI one above was
	// pinned, so a change to it moved every us-geo Anthropic dollar with no test
	// naming the number. Both multipliers are now also inside pricingIdentity.
	if multiplier, ok := catalog.PricingMultiplier("anthropic", "claude-sonnet-4-6", "global", "inference_geo_us_multiplier"); !ok || multiplier != 1.1 {
		t.Fatalf("Anthropic us-geo multiplier = %v/%v, want 1.1/true", multiplier, ok)
	}
	if _, ok := catalog.PricingMultiplier("anthropic", "claude-haiku-4-5", "global", "inference_geo_us_multiplier"); ok {
		t.Fatal("a model with no documented us-geo premium inherited one")
	}
}

// List must return the full catalog (more than the old hardcoded 3 models) and
// span every provider — so the /providers/catalog endpoint serves the live
// catalog, not a static subset.
// TestBedrockClaudeCacheRowsAreFullyPriced is slice C's catalog-completeness
// gate (AUTOPILOT_SPEC §9 V1 condition 6): verified cache math cannot run on a
// partially-priced model, so every row the cache-point transform can actually
// inject must carry BOTH cache rates. The population filter is the RUNTIME'S
// OWN predicate — bedrock.CachePointEligibleModel, the exact function
// cache_points.go gates on — so the transform's population and this test's
// population are the same set by construction (a looser matcher here once hid
// that the runtime's set differed from the audited one). Since 2026-08-02 the
// predicate strips one inference-profile routing scope before the Claude
// match, so catalog-priced global.* profile rows ARE in population; profile
// ids with no catalog row of their own (us./eu. geographic profiles today)
// stay out, and models routed but unpriced stay handled by the honest zero —
// Price returns `unpriced:` and the verified gate excludes them. The 1h write
// rate is not required here: Bedrock's cache TTL is 5
// minutes, and the gateway's runtime guard zeroes the delta if a 1h bucket
// ever appears against a missing rate.
func TestBedrockClaudeCacheRowsAreFullyPriced(t *testing.T) {
	seen := 0
	for _, e := range catalog.List() {
		if e.Provider != "bedrock" || !bedrock.CachePointEligibleModel(e.Model) {
			continue
		}
		seen++
		if e.Pricing.CacheReadPerMillion <= 0 || e.Pricing.CacheWritePerMillion <= 0 {
			t.Errorf("bedrock/%s is cache-point eligible but partially priced (read=%v write=%v) — verified math cannot run on it",
				e.Model, e.Pricing.CacheReadPerMillion, e.Pricing.CacheWritePerMillion)
		}
	}
	if seen == 0 {
		t.Fatal("no cache-point-eligible Bedrock Claude rows found — the verified Bedrock method has no priced population")
	}
}

func TestListReturnsFullCatalog(t *testing.T) {
	entries := catalog.List()
	if len(entries) <= 3 {
		t.Fatalf("List returned %d entries, want the full catalog (>3)", len(entries))
	}
	providers := map[string]bool{}
	for _, e := range entries {
		providers[e.Provider] = true
	}
	for _, want := range []string{"openai", "anthropic", "gemini", "bedrock", "vertex"} {
		if !providers[want] {
			t.Errorf("catalog missing provider %q (List must cover all providers, not just 3)", want)
		}
	}
}

// TestPriceAffectingRegistryIsExactlyPinned makes every change to the registry
// a deliberate, reviewed one, and records the KNOWN RESIDUAL so it is a named
// boundary rather than an unknown.
//
// In scope (registered, pinned by the immutable snapshot): capabilities that
// change what a request COSTS — the two rate multipliers and the flag deciding
// whether a global row's price answers a regional lookup.
//
// Deliberately out of scope, and the residual to keep in view: the worker's
// detector layer (cloud/worker/internal/detectors/detectors.go, catalogGenerationModel
// and catalogContextFits) reads responses_api / chat_completions / messages_api /
// generate_content / context_window_tokens / max_input_tokens to pick which
// cheaper model a Cave Plan move proposes. Those keys do not change any row's
// price — they change which row's price gets compared — so they move INFERRED
// HEADROOM, never measured spend and never a signed receipt. That is a weaker
// class of wrongness and a different owner's package, so they stay unregistered
// on purpose. If a capability is ever read to compute a figure that reaches a
// receipt, it belongs in this registry and this test's list.
func TestPriceAffectingRegistryIsExactlyPinned(t *testing.T) {
	want := []string{
		"regional_processing_multiplier",
		"inference_geo_us_multiplier",
		"region_agnostic_pricing",
	}
	if !reflect.DeepEqual(catalog.PriceAffectingCapabilities, want) {
		t.Fatalf("PriceAffectingCapabilities = %v, want %v (adding one is a money decision: it must also be pinned by the snapshot and mirrored in validate_catalog.py)",
			catalog.PriceAffectingCapabilities, want)
	}
}

// TestPricingFunctionsReadNoUnregisteredCapabilityLiteral is the structural
// guard behind the registry. PricingMultiplier fails closed on an unregistered
// key, but that only binds callers who go THROUGH PricingMultiplier — a direct
// entry.Capabilities["..."] lookup bypasses it entirely, which is exactly how
// region_agnostic_pricing kept moving money outside the pin after the two
// multipliers were fixed. catalog.List() also hands raw capability maps to every
// caller, so this cannot be enforced repo-wide from here; what it can enforce is
// that THIS file's pricing functions never reintroduce the pattern.
func TestPricingFunctionsReadNoUnregisteredCapabilityLiteral(t *testing.T) {
	source, err := os.ReadFile("catalog.go")
	if err != nil {
		t.Fatal(err)
	}
	// Strip line comments first: the prose in catalog.go names the very pattern
	// it forbids, and a scanner that flags its own documentation is useless.
	code := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(string(source), "")
	// EVERY string-literal capability lookup is rejected, registered or not. A
	// literal read bypasses priceAffectingBool's guard even when the key happens
	// to be registered today, and "happens to be correct right now" is precisely
	// the state region_agnostic_pricing was in before the pin caught up with it.
	for _, match := range regexp.MustCompile(`Capabilities\["([^"]+)"\]`).FindAllStringSubmatch(code, -1) {
		t.Errorf("catalog.go reads capability %q by string literal; pricing functions must read capabilities through priceAffectingBool/PricingMultiplier so an unregistered key fails closed instead of silently pricing off a value the snapshot does not pin", match[1])
	}
}
