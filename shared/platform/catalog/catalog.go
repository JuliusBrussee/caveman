package catalog

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/cost"
	"gopkg.in/yaml.v3"
)

type Entry struct {
	Provider     string         `yaml:"provider"`
	Model        string         `yaml:"model"`
	Region       string         `yaml:"region"`
	Currency     string         `yaml:"currency"`
	Pricing      cost.Price     `yaml:"pricing"`
	Capabilities map[string]any `yaml:"capabilities"`
	Sources      []string       `yaml:"sources"`
	// VerifiedAt means exactly one thing: the date this row's PRICING was last
	// checked against the vendor's published pricing page. It is the sole input
	// to catalogVersion() below, which is embedded in cost reports and signed
	// receipts as a price-provenance attestation — nothing else may bump it.
	// The immutable-dated-snapshot mechanism
	// (TestEveryCurrentCatalogRowMatchesItsImmutableDatedSnapshot) is keyed off
	// this field and pins the pricing-relevant columns (provider, model,
	// region, currency, pricing, verified_at) plus the price multipliers listed
	// on PricingMultiplier below, which are price wearing a capability's
	// clothes. Every OTHER capability, plus Sources and CapabilitiesVerifiedAt,
	// is free to differ from the archived snapshot.
	VerifiedAt string `yaml:"verified_at"`
	// CapabilitiesVerifiedAt means the date this row's capability booleans
	// (tools/vision/json_mode) and context_window_tokens were last checked
	// against the vendor's own model docs. It is deliberately NOT read by
	// catalogVersion() or any cost/receipt path — capability provenance must
	// never be mistaken for, or silently promote, price provenance. Optional:
	// older immutable snapshots predate this field and decode with it empty.
	CapabilitiesVerifiedAt string `yaml:"capabilities_verified_at,omitempty"`
}

var (
	entries []Entry
	once    sync.Once
	loadErr error
)

// Price returns the catalog pricing for an exact provider+model pair along with
// the catalog version (the entry's verified_at date). When a model is not in the
// catalog it returns a zero price and an "unpriced:" version rather than
// silently borrowing another model's price — an unknown model produces an
// honestly-zero cost that is visibly flagged instead of a plausible-but-wrong
// number.
func Price(provider, model string) (cost.Price, string) {
	once.Do(load)
	for _, entry := range entries {
		if entry.Provider == provider && entry.Model == model {
			// Generic callers may use only an explicit global rate. Borrowing the
			// first regional row is order-dependent and can fabricate spend.
			if entry.Region == "global" {
				return entry.Pricing, catalogVersion(entry.VerifiedAt)
			}
		}
	}
	if loadErr != nil {
		return cost.Price{}, "unpriced:catalog-invalid/" + provider + "/" + model
	}
	return cost.Price{}, "unpriced:" + provider + "/" + model
}

// PriceForRegion returns only an exact provider+model+region catalog row. It is
// the runtime billing lookup for region-sensitive Bedrock and Vertex calls;
// falling back to another region would create plausible but wrong spend.
func PriceForRegion(provider, model, region string) (cost.Price, string) {
	once.Do(load)
	for _, entry := range entries {
		if entry.Provider == provider && entry.Model == model && entry.Region == region {
			return entry.Pricing, catalogVersion(entry.VerifiedAt)
		}
	}
	if loadErr != nil {
		return cost.Price{}, "unpriced:catalog-invalid/" + provider + "/" + model + "@" + region
	}
	return cost.Price{}, "unpriced:" + provider + "/" + model + "@" + region
}

// RegionAgnosticPricingCapability lets a global row's price answer a regional
// lookup. It reads like capability data and is not: flipping it either way
// changes what a request costs (see PriceAffectingCapabilities).
const RegionAgnosticPricingCapability = "region_agnostic_pricing"

// PriceAffectingCapabilities are the capability keys that are PRICE, not
// capability data. They live under `capabilities` for schema reasons only:
// callers multiply a row's token rates by them, or use them to decide which
// row's price applies at all, so editing one moves real dollars while every
// pricing column and verified_at stay untouched.
//
// This slice is the single source of truth. The immutable dated snapshot pins
// exactly these keys (catalog_test.go reads this variable directly, and
// validate_catalog.py's PRICE_AFFECTING_CAPABILITY_KEYS is asserted equal to it
// by a cross-language test), and PricingMultiplier refuses any capability that
// is not in it. That refusal is the enforcement: a new price-affecting
// capability read through this package either appears here — and is therefore
// pinned — or it does not work at all. Prose asking a future author to "also
// update the other list" is what let region_agnostic_pricing escape the pin
// after the multipliers were fixed; a list nobody can forget replaces it.
var PriceAffectingCapabilities = []string{
	"regional_processing_multiplier",
	"inference_geo_us_multiplier",
	RegionAgnosticPricingCapability,
}

func priceAffecting(capability string) bool {
	for _, key := range PriceAffectingCapabilities {
		if key == capability {
			return true
		}
	}
	return false
}

// priceAffectingBool is the ONLY way a pricing function in this file may read a
// boolean capability. It returns false for anything unregistered, so a capability
// that decides a price but is not pinned by the snapshot simply does not apply —
// the request falls through to an honest `unpriced:` zero instead of being priced
// off a value the receipt's catalog_version does not attest.
//
// TestPricingFunctionsReadNoUnregisteredCapabilityLiteral scans this file for
// raw Capabilities["..."] lookups, so the next author cannot reintroduce the
// direct map read that let region_agnostic_pricing escape the pin.
func priceAffectingBool(capabilities map[string]any, capability string) bool {
	if !priceAffecting(capability) {
		return false
	}
	allowed, ok := capabilities[capability].(bool)
	return ok && allowed
}

// PriceForRegionOrAgnostic first requires an exact regional row, then permits a
// global row only when that row explicitly declares region_agnostic_pricing.
// This is intended for APIs whose public list price is location-independent;
// callers must not silently apply it to marketplace/regional premiums.
func PriceForRegionOrAgnostic(provider, model, region string) (cost.Price, string) {
	once.Do(load)
	for _, entry := range entries {
		if entry.Provider == provider && entry.Model == model && entry.Region == region {
			return entry.Pricing, catalogVersion(entry.VerifiedAt)
		}
	}
	for _, entry := range entries {
		if entry.Provider == provider && entry.Model == model && entry.Region == "global" {
			if priceAffectingBool(entry.Capabilities, RegionAgnosticPricingCapability) {
				return entry.Pricing, catalogVersion(entry.VerifiedAt)
			}
		}
	}
	if loadErr != nil {
		return cost.Price{}, "unpriced:catalog-invalid/" + provider + "/" + model + "@" + region
	}
	return cost.Price{}, "unpriced:" + provider + "/" + model + "@" + region
}

// PricingMultiplier returns a positive model/region pricing capability from an
// exact catalog row. A missing or malformed capability is unavailable; callers
// must not invent a provider-wide multiplier.
//
// It fails closed on any capability not registered in
// PriceAffectingCapabilities. A multiplier the snapshot does not pin would move
// money the catalog_version in a signed receipt does not attest, so an
// unregistered one must not work at all — registering it is what pins it.
func PricingMultiplier(provider, model, region, capability string) (float64, bool) {
	if !priceAffecting(capability) {
		return 0, false
	}
	once.Do(load)
	for _, entry := range entries {
		if entry.Provider != provider || entry.Model != model || entry.Region != region {
			continue
		}
		value, ok := entry.Capabilities[capability].(float64)
		return value, ok && value > 0
	}
	return 0, false
}

// List returns every catalog entry so callers can serve the real, complete model
// catalog — provider/model/capabilities — instead of a hardcoded subset. The order
// matches current.yaml. Each entry's Capabilities map is deep-copied so a caller
// can never mutate the package-global cached catalog.
func List() []Entry {
	once.Do(load)
	out := make([]Entry, len(entries))
	copy(out, entries)
	for i := range out {
		if out[i].Capabilities != nil {
			caps := make(map[string]any, len(out[i].Capabilities))
			for k, v := range out[i].Capabilities {
				caps[k] = v
			}
			out[i].Capabilities = caps
		}
		out[i].Sources = append([]string(nil), out[i].Sources...)
	}
	return out
}

func catalogVersion(verifiedAt string) string {
	if len(verifiedAt) >= 10 {
		return verifiedAt[:10]
	}
	if verifiedAt != "" {
		return verifiedAt
	}
	return "unknown"
}

func load() {
	for _, path := range catalogCandidates() {
		raw, err := os.ReadFile(path)
		if err != nil {
			loadErr = err
			continue
		}
		decoded, err := DecodeAndValidate(raw)
		if err == nil {
			entries = decoded
			loadErr = nil
			return
		}
		loadErr = fmt.Errorf("catalog %s: %w", path, err)
	}
	entries = []Entry{}
}

// DecodeAndValidate strictly parses a catalog. Unknown YAML fields, duplicate
// provider/model/region rows, invalid URLs/timestamps, and unsafe prices reject
// the entire file; runtime must never load a plausible partial catalog.
func DecodeAndValidate(raw []byte) ([]Entry, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var decoded []Entry
	if err := dec.Decode(&decoded); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple YAML documents are not allowed")
		}
		return nil, err
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("catalog is empty")
	}
	seen := make(map[string]struct{}, len(decoded))
	for i, entry := range decoded {
		label := fmt.Sprintf("entry %d", i)
		if strings.TrimSpace(entry.Provider) == "" || strings.TrimSpace(entry.Model) == "" || strings.TrimSpace(entry.Region) == "" {
			return nil, fmt.Errorf("%s: provider, model, and region are required", label)
		}
		if entry.Currency != "USD" {
			return nil, fmt.Errorf("%s %s/%s: unsupported currency %q", label, entry.Provider, entry.Model, entry.Currency)
		}
		if !cost.ValidPrice(entry.Pricing) {
			return nil, fmt.Errorf("%s %s/%s: invalid pricing", label, entry.Provider, entry.Model)
		}
		verified, err := time.Parse(time.RFC3339, entry.VerifiedAt)
		if err != nil || verified.IsZero() {
			return nil, fmt.Errorf("%s %s/%s: verified_at must be RFC3339", label, entry.Provider, entry.Model)
		}
		if verified.After(time.Now().UTC().Add(24 * time.Hour)) {
			return nil, fmt.Errorf("%s %s/%s: verified_at is in the future", label, entry.Provider, entry.Model)
		}
		if entry.CapabilitiesVerifiedAt != "" {
			capVerified, err := time.Parse(time.RFC3339, entry.CapabilitiesVerifiedAt)
			if err != nil || capVerified.IsZero() {
				return nil, fmt.Errorf("%s %s/%s: capabilities_verified_at must be RFC3339", label, entry.Provider, entry.Model)
			}
			if capVerified.After(time.Now().UTC().Add(24 * time.Hour)) {
				return nil, fmt.Errorf("%s %s/%s: capabilities_verified_at is in the future", label, entry.Provider, entry.Model)
			}
		}
		if len(entry.Sources) == 0 {
			return nil, fmt.Errorf("%s %s/%s: at least one source is required", label, entry.Provider, entry.Model)
		}
		for _, rawURL := range entry.Sources {
			u, err := url.ParseRequestURI(rawURL)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return nil, fmt.Errorf("%s %s/%s: invalid HTTPS source %q", label, entry.Provider, entry.Model, rawURL)
			}
		}
		key := entry.Provider + "\x00" + entry.Model + "\x00" + entry.Region
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate provider/model/region row %s/%s@%s", entry.Provider, entry.Model, entry.Region)
		}
		seen[key] = struct{}{}
	}
	return decoded, nil
}

// catalogCandidates lists the paths to try, in order: an explicit env override,
// the deploy-image and CWD-relative locations, then a walk up from the working
// directory so the catalog resolves when binaries or tests run from subdirs.
// Both repo layouts are tried: the monorepo keeps the catalog under public/,
// the published caveman repo has it at the top level.
func catalogCandidates() []string {
	rels := []string{
		"public/shared/provider-catalog/catalog/current.yaml",
		"shared/provider-catalog/catalog/current.yaml",
	}
	candidates := []string{}
	if p := os.Getenv("CAVE_CATALOG_PATH"); p != "" {
		// Explicit override is authoritative. A bad override fails closed instead
		// of silently falling back to a different on-disk catalog.
		return []string{p}
	}
	for _, rel := range rels {
		candidates = append(candidates, rel, "/app/"+rel)
	}
	if wd, err := os.Getwd(); err == nil {
		dir := wd
		for i := 0; i < 8; i++ {
			for _, rel := range rels {
				candidates = append(candidates, filepath.Join(dir, rel))
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return candidates
}
