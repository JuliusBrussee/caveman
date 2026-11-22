package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

// toolSchemaStripOptimizerID labels the tool-schema annotation strip in
// x-cave-optimization and in the telemetry row's optimization list, so a request
// whose catalog was rewritten is never indistinguishable from one that was not.
const toolSchemaStripOptimizerID = "toolschema-annotation-strip"

// toolSchemaShrinkOptimizerID labels opt-in caveman-shrink compression of the
// tool catalog (`toolschema_strip: shrink` / `CAVEMAN_TOOLSCHEMA_STRIP=shrink`).
const toolSchemaShrinkOptimizerID = "toolschema-shrink"

// toolSchemaStripMode enables annotation-only stripping; toolSchemaShrinkMode
// enables full caveman-shrink description + annotation compression.
const (
	toolSchemaStripMode  = "annotations"
	toolSchemaShrinkMode = "shrink"
)

// toolSchemaStripVersion and toolSchemaShrinkVersion pin each mode's SEMANTICS
// into the prefix-cache scope key so switching modes performs a clean one-time
// epoch rollover instead of a silent partial cold write.
const (
	toolSchemaStripVersion  = "v1"
	toolSchemaShrinkVersion = "shrink-v1"
)

func toolSchemaVersionForMode(mode string) string {
	switch mode {
	case toolSchemaStripMode:
		return toolSchemaStripVersion
	case toolSchemaShrinkMode:
		return toolSchemaShrinkVersion
	default:
		return ""
	}
}

func (s *Server) toolSchemaOptimizerID() string {
	if s.toolSchemaStrip == toolSchemaShrinkMode {
		return toolSchemaShrinkOptimizerID
	}
	return toolSchemaStripOptimizerID
}

// toolSchemaStripAllowed reports whether the configured tool-schema rewrite may run.
func (s *Server) toolSchemaStripAllowed(adapter providers.Adapter, body []byte, sessionID string) bool {
	return s.toolSchemaStripApplies(adapter, body) && s.ledger.LeverAllowed(sessionID, leverToolSchemaStrip)
}

// toolSchemaStripApplies is toolSchemaStripAllowed without the ledger: the
// rewrite would run here if the session's harm tripwire had not frozen it.
func (s *Server) toolSchemaStripApplies(adapter providers.Adapter, body []byte) bool {
	if s.compressor == nil {
		return false
	}
	switch s.toolSchemaStrip {
	case toolSchemaStripMode:
		if _, ok := s.compressor.(ToolSchemaStripper); !ok {
			return false
		}
	case toolSchemaShrinkMode:
		if _, ok := s.compressor.(ToolSchemaShrinker); !ok {
			return false
		}
	default:
		return false
	}
	return s.liveZoneCompressionAllowed(adapter, body)
}

// toolSchemaCacheScope returns the suffix the strip/shrink contributes to a
// prefix-cache scope key. It is empty when the lever is off, so every
// pre-existing scope key is byte-identical to what it was before this lever existed.
func (s *Server) toolSchemaCacheScope() string {
	if v := toolSchemaVersionForMode(s.toolSchemaStrip); v != "" {
		return "|tss:" + v
	}
	return ""
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
//
// Purity of the strip is not enough on its own, because storing the original can
// fail: the first decision for a catalog — stripped or original — is memoised in
// the PrefixCache and re-sent from then on, so a recovery-store hiccup can never
// flip the head of the prefix. A hit splices the stored bytes without storing
// again. The ledger freeze is the one deliberate exception: a session whose
// tripwire froze the lever falls back to the original catalog once, by design.
func (s *Server) stripToolSchema(body []byte, meta providers.RequestMetadata, requestID string) ([]byte, string, bool) {
	if s.prefixCache == nil || s.compressor == nil {
		return nil, "", false
	}
	var transformCatalog func([]byte) ([]byte, bool)
	switch s.toolSchemaStrip {
	case toolSchemaStripMode:
		stripper, ok := s.compressor.(ToolSchemaStripper)
		if !ok {
			return nil, "", false
		}
		transformCatalog = stripper.StripToolSchema
	case toolSchemaShrinkMode:
		shrinker, ok := s.compressor.(ToolSchemaShrinker)
		if !ok {
			return nil, "", false
		}
		transformCatalog = shrinker.ShrinkToolSchema
	default:
		return nil, "", false
	}
	raw, reassemble, ok := providers.ExtractToolCatalog(body, meta)
	if !ok {
		return nil, "", false
	}
	scope := "toolschema" + s.toolSchemaCacheScope()
	stripped, handle, hit := s.prefixCache.LookupReplacement(scope, raw)
	if hit && handle == RawDecisionHandle {
		return nil, "", false
	}
	if !hit {
		if s.unpersistedRaw.has(scope, raw) {
			// Sent original while the store could not record it; a stored row
			// outranks that, so record it now and follow what comes back.
			stripped, handle = nil, RawDecisionHandle
		} else {
			var ok bool
			if stripped, ok = transformCatalog(raw); !ok || len(stripped) >= len(raw) {
				return nil, "", false // removes nothing, on every turn
			}
			var err error
			handle, err = s.compressor.StoreOriginal(raw)
			if err != nil || handle == "" {
				if s.logger != nil {
					s.logger.Warn("tool-schema recovery store failed; keeping the original catalog",
						"error", redact.Error(err), "request_id", requestID)
				}
				stripped, handle = nil, RawDecisionHandle
			}
		}
		stored, err := s.prefixCache.RememberReplacement(scope, raw, stripped, handle)
		if err != nil {
			s.unpersistedRaw.add(scope, raw)
			return nil, "", false
		}
		if len(stored) == 0 {
			return nil, "", false
		}
		if !bytes.Equal(stored, stripped) {
			if stripped, handle, hit = s.prefixCache.LookupReplacement(scope, raw); !hit || handle == RawDecisionHandle {
				return nil, "", false
			}
		}
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
