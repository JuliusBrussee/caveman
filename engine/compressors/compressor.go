// Package compressors holds the engine's content-type compressors and the
// registry that routes a content type to one. A compressor is a pure byte
// transform: it never counts tokens, stores recoveries, or talks to the
// network — the engine core does that around it. This keeps each compressor a
// self-contained, testable module and is why adding one is just a new file + its
// tests.
package compressors

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

// Compressor compresses one content type. Every compressor is structural,
// deterministic, idempotent, and fail-closed: on any parse problem it returns
// ok=false and the caller forwards the original bytes unchanged. Byte safety is
// derived from the compressor's safety class, not assumed for every compressor.
type Compressor interface {
	// ContentType is the type this compressor handles (e.g. "json").
	ContentType() string
	// SafetyClass is the compressor's inherent class on the S0–S4 ladder.
	SafetyClass() safety.Class
	// Compress returns the compressed bytes with ok=true on success. On any
	// parse problem (malformed input, an unsupported shape) it returns
	// (nil-or-input, false) and the caller MUST forward the original unchanged.
	Compress(input []byte) (out []byte, ok bool)
}

// Metadata describes what a compressor actually emitted for one successful
// transform. Composite compressors use it to report their chosen method.
type Metadata struct {
	Method           string
	LosslessToModel  *bool
	RecoveryMetadata []byte
}

// MetadataCompressor is an optional capability for compressors that can report
// per-result method metadata, or whose method differs from ContentType.
type MetadataCompressor interface {
	Compressor
	CompressWithMetadata(input []byte, query string) (out []byte, meta Metadata, ok bool)
}

func metadataBool(v bool) *bool { return &v }

// QueryAwareCompressor is an optional capability a compressor may implement to
// bias its output toward a query. The engine type-asserts for it and calls
// CompressQuery only when Options.Query is non-empty; compressors that do not
// implement it are unaffected and keep using Compress. Fail-closed behavior is unchanged:
// CompressQuery still returns ok=false on any parse problem so the caller
// forwards the original bytes unchanged, and a query never makes the output
// larger than the query-agnostic result would be.
type QueryAwareCompressor interface {
	Compressor
	// CompressQuery is Compress with an additional relevance query. An empty
	// query must behave exactly like Compress.
	CompressQuery(input []byte, query string) (out []byte, ok bool)
}

// Registry maps content types to compressors.
type Registry struct {
	m map[string]Compressor
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{m: map[string]Compressor{}} }

// Register adds a compressor, keyed by its content type. A later registration
// for the same type replaces the earlier one.
func (r *Registry) Register(c Compressor) { r.m[c.ContentType()] = c }

// For returns the compressor for a content type, or (nil, false).
func (r *Registry) For(contentType string) (Compressor, bool) {
	c, ok := r.m[contentType]
	return c, ok
}

// Default returns a registry with the engine's built-in compressors registered:
// JSON, log, code, diff, search-result, text, HTML, tabular, config, tool-schema,
// lossless tool-schema, TOON, accessibility-tree, repetition, and terminal. The
// code compressor is selected at build time — a tree-sitter-backed one when cgo is
// enabled, a pure-Go go/ast one otherwise. HTML and terminal are auto-detected
// (Detect → "html"/"terminal"); tool-schema, lossless tool-schema, TOON,
// accessibility-tree, and repetition are never auto-detected and are reached only
// by forcing Options.Type.
func Default() *Registry {
	r := NewRegistry()
	r.Register(NewJSON())
	r.Register(NewLog())
	r.Register(newCode())
	r.Register(NewDiff())
	r.Register(NewSearchResult())
	r.Register(NewText())
	r.Register(NewHTML())
	r.Register(NewTabular())
	r.Register(NewConfig())
	r.Register(NewToolSchema())
	r.Register(NewToolSchemaAnnotations())
	r.Register(NewTOON())
	r.Register(NewAXTree())
	r.Register(NewRepetition())
	r.Register(NewTerminal())
	return r
}

// CapabilityRegistry is the public, content-blind transform ABI consumed by
// Cave Compiler. Its JSON field names match transform-capability.schema.json.
type CapabilityRegistry struct {
	SchemaVersion  int          `json:"schema_version"`
	RegistrySHA256 string       `json:"registry_sha256"`
	Capabilities   []Capability `json:"capabilities"`
}

// Capability describes one deterministic engine transform. Content bytes and
// customer identifiers never enter this manifest.
type Capability struct {
	TransformID           string     `json:"transform_id"`
	ImplementationVersion string     `json:"implementation_version"`
	SafetyClasses         []string   `json:"safety_classes"`
	Deterministic         bool       `json:"deterministic"`
	Recovery              string     `json:"recovery"`
	EligibleSegmentKinds  []string   `json:"eligible_segment_kinds"`
	RequiresEval          bool       `json:"requires_eval"`
	NotSmallerFallback    string     `json:"not_smaller_fallback"`
	Provenance            Provenance `json:"provenance"`
	ConformanceDigest     string     `json:"conformance_digest"`
}

// Provenance pins whether source is native or externally derived.
type Provenance struct {
	Kind                 string  `json:"kind"`
	SourceManifestSHA256 *string `json:"source_manifest_sha256"`
}

// manifestExcluded are compressors that are registered — reachable by forcing
// Options.Type — but deliberately NOT advertised in the transform-capability ABI
// Cave Compiler consumes. Advertising one rotates RegistrySHA256, which every
// already-built Cave Build lock pins; a lock that no longer matches fails the
// agent at RUN time. A compressor belongs here when a compiled plan can never
// route to it anyway, so the ABI would gain a row it can never use in exchange
// for invalidating every lock in the field.
var manifestExcluded = map[string]bool{
	toolSchemaAnnotationsType: true,
}

// Capabilities exports the default engine registry in stable transform-ID
// order. Unknown safety classes fail closed by returning no capability.
func (r *Registry) Capabilities() []Capability {
	out := make([]Capability, 0, len(r.m))
	for contentType, compressor := range r.m {
		if manifestExcluded[contentType] {
			continue
		}
		class := compressor.SafetyClass()
		if !class.Valid() {
			return nil
		}
		conformance := sha256.Sum256([]byte("caveman.engine." + contentType + ":v1"))
		out = append(out, Capability{
			TransformID:           "caveman.engine." + contentType + ".v1",
			ImplementationVersion: "1",
			SafetyClasses:         []string{class.String()},
			Deterministic:         true,
			Recovery:              "exact_ccr",
			EligibleSegmentKinds:  eligibleSegments(contentType),
			RequiresEval:          true,
			NotSmallerFallback:    "original",
			Provenance: Provenance{
				Kind:                 "native",
				SourceManifestSHA256: nil,
			},
			ConformanceDigest: hex.EncodeToString(conformance[:]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TransformID < out[j].TransformID })
	return out
}

// CapabilityManifest returns canonical compact JSON. RegistrySHA256 hashes the
// ordered capabilities only, avoiding a self-referential digest.
func (r *Registry) CapabilityManifest() ([]byte, error) {
	capabilities := r.Capabilities()
	if capabilities == nil {
		return nil, safetyClassError{}
	}
	canonical, err := json.Marshal(capabilities)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(canonical)
	return json.Marshal(CapabilityRegistry{
		SchemaVersion:  1,
		RegistrySHA256: hex.EncodeToString(digest[:]),
		Capabilities:   capabilities,
	})
}

type safetyClassError struct{}

func (safetyClassError) Error() string { return "compressors: unknown safety class" }

func eligibleSegments(contentType string) []string {
	switch contentType {
	case "toolschema":
		return []string{"tool_schema"}
	case "a11y":
		return []string{"artifact", "tool_result"}
	case "repetition":
		return []string{"history", "tool_result"}
	default:
		return []string{"artifact", "history", "skill", "tool_result"}
	}
}
