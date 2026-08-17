package gateway

import (
	"encoding/json"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

// toolSchemaStripOptimizerID labels the tool-schema annotation strip in
// x-cave-optimization and in the telemetry row's optimization list, so a request
// whose catalog was rewritten is never indistinguishable from one that was not.
const toolSchemaStripOptimizerID = "toolschema-annotation-strip"

// toolSchemaStripMode is the one value that enables the lever. It names what is
// removed — JSON-Schema annotation keywords — rather than claiming losslessness.
const toolSchemaStripMode = "annotations"

// toolSchemaStripVersion pins the strip's SEMANTICS into the prefix-cache scope
// key. Turning the lever on, off, or changing which keywords it removes moves the
// tool catalog, which is the head of the provider cache prefix; message
// replacements memoised against the old catalog would then be re-substituted
// under a prefix that no longer matches. Folding this into the scope makes such a
// change a deliberate, one-time epoch rollover instead of a silent partial cold
// write. Bump it whenever annotationDropKeys or the descent allowlist changes.
const toolSchemaStripVersion = "v1"

// toolSchemaStripAllowed reports whether the tool-schema annotation strip may run.
//
// The strip changes model-visible bytes, so it may default on only
// under the local-wrap clause (recovery + CCR) — and it does not even do that: it
// is default OFF and an operator turns it on explicitly with
// `toolschema_strip: annotations` / CAVEMAN_TOOLSCHEMA_STRIP=annotations. On top
// of the flag it reuses liveZoneCompressionAllowed rather than restating its
// conditions, so it inherits the same three fail-closed local-wrap requirements —
// the operator off-switch, MCP recovery through the agent's own caveman_retrieve,
// and a durable prefix cache behind a schema-aware adapter. A Compressor that
// cannot strip, or cannot store the original for recovery, keeps the catalog
// unchanged.
//
// On top of all of that it consults the session ledger's freeze registry — it is
// the first lever to do so. A session whose measured cache-creation blew up after
// this strip ran loses the strip for the rest of that session, and falls open to
// forwarding the catalog unchanged.
func (s *Server) toolSchemaStripAllowed(adapter providers.Adapter, sessionID string) bool {
	if s.toolSchemaStrip != toolSchemaStripMode || s.compressor == nil {
		return false
	}
	if _, ok := s.compressor.(ToolSchemaStripper); !ok {
		return false
	}
	if !s.ledger.LeverAllowed(sessionID, leverToolSchemaStrip) {
		return false
	}
	return s.liveZoneCompressionAllowed(adapter)
}

// toolSchemaCacheScope returns the suffix the strip contributes to a prefix-cache
// scope key. It is empty when the lever is off, so every pre-existing scope key
// is byte-identical to what it was before this lever existed.
func (s *Server) toolSchemaCacheScope() string {
	if s.toolSchemaStrip != toolSchemaStripMode {
		return ""
	}
	return "|tss:" + toolSchemaStripVersion
}

// stripToolSchema removes the documentation-only JSON-Schema annotations from the
// request's tool catalog and returns the rewritten body plus the CCR handle of the
// exact original catalog bytes.
//
// The tool catalog is the very front of the provider cache prefix, which the
// general compress path deliberately never touches: no adapter exposes it through
// ExtractStabilizable, and compressRequest's `!block.live` guard exists to keep
// frozen bytes out of the rewrite loop. This is a separate path with a separate
// invariant. The strip is admissible in the frozen prefix ONLY because it is a
// deterministic pure function of the catalog bytes: identical tools produce
// identical stripped bytes on every turn, every session, and every restart, so the
// prefix the first request paid to cache is the exact prefix every later request
// re-sends. Nothing here may become input-dependent on turn number, time, or
// process state without breaking that.
//
// Every failure keeps the original catalog: a shape we cannot extract, a strip
// that removed nothing, an unstorable original, a failed splice, or output that is
// not valid JSON.
func (s *Server) stripToolSchema(body []byte, meta providers.RequestMetadata, requestID string) ([]byte, string, bool) {
	stripper, ok := s.compressor.(ToolSchemaStripper)
	if !ok {
		return nil, "", false
	}
	raw, reassemble, ok := providers.ExtractToolCatalog(body, meta)
	if !ok {
		return nil, "", false
	}
	stripped, ok := stripper.StripToolSchema(raw)
	if !ok || len(stripped) >= len(raw) {
		return nil, "", false
	}
	handle, err := s.compressor.StoreOriginal(raw)
	if err != nil || handle == "" {
		if s.logger != nil {
			s.logger.Warn("tool-schema recovery store failed; keeping the original catalog",
				"error", redact.Error(err), "request_id", requestID)
		}
		return nil, "", false
	}
	out, err := reassemble(stripped)
	if err != nil || len(out) == 0 || !json.Valid(out) {
		if s.logger != nil {
			s.logger.Warn("tool-schema splice failed; forwarding original bytes unchanged",
				"error", redact.Error(err), "request_id", requestID)
		}
		return nil, "", false
	}
	return out, handle, true
}
