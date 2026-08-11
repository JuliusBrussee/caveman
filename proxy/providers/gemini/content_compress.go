package gemini

import (
	"sort"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

const minCompressBlockBytes = 512

// zoneCandidate is one splice candidate plus the provider-cache zone it sits in.
// Live candidates belong to the just-arrived user turn and may be newly compressed;
// frozen candidates are the same fields in EARLIER user turns, exposed only so a
// replacement this proxy already emitted for those exact bytes can be substituted
// back byte-identically.
type zoneCandidate struct {
	jsonsplice.Candidate
	live bool
	kind string
}

type typedCandidate struct {
	jsonsplice.Candidate
	kind string
}

// ExtractCompressible returns only Gemini's live zone: the text parts and direct
// functionResponse output fields of the latest user turn. It never collects model
// turns or systemInstruction, and reassembly byte-splices changed string values
// into the original request bytes.
func (a Adapter) ExtractCompressible(body []byte, meta providers.RequestMetadata) ([][]byte, func([][]byte) ([]byte, error), bool) {
	zones, ok := rewritableZones(body, meta, true)
	if !ok {
		return nil, nil, false
	}
	candidates := make([]jsonsplice.Candidate, 0, len(zones))
	for _, zone := range zones {
		if zone.live {
			candidates = append(candidates, zone.Candidate)
		}
	}
	if len(candidates) == 0 {
		return nil, nil, false
	}
	segments := make([][]byte, len(candidates))
	for i, candidate := range candidates {
		segments[i] = append([]byte(nil), candidate.Original...)
	}
	return segments, func(replacements [][]byte) ([]byte, error) { return jsonsplice.Replace(body, candidates, replacements) }, true
}

// ExtractStabilizable returns every content block the proxy may rewrite, each
// tagged with the cache zone it sits in: the latest user turn (Live, eligible for
// new compression) plus the same fields in earlier user turns (frozen — eligible
// ONLY for byte-identical substitution of a replacement the proxy already emitted
// for those exact bytes).
//
// Frozen blocks have to be exposed because compression is not a one-turn event: a
// turn compressed while it WAS the live zone arrives again on the next request as
// the client's original bytes. Gemini caches repeated prompt prefixes implicitly,
// so forwarding those originals silently misses the cache entry the previous turn
// paid to create — the caller substitutes the stored replacement instead and the
// prefix stays byte-stable for the whole conversation. Model turns are never
// collected: the proxy never compresses them, so a substitution could never exist
// for them.
func (a Adapter) ExtractStabilizable(body []byte, meta providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool) {
	zones, ok := rewritableZones(body, meta, false)
	if !ok || len(zones) == 0 {
		return nil, nil, false
	}
	candidates := make([]jsonsplice.Candidate, len(zones))
	blocks := make([]providers.RewritableBlock, len(zones))
	for i, zone := range zones {
		candidates[i] = zone.Candidate
		blocks[i] = providers.RewritableBlock{
			Content: append([]byte(nil), zone.Original...),
			Live:    zone.live,
			Kind:    zone.kind,
		}
	}
	return blocks, func(replacements [][]byte) ([]byte, error) { return jsonsplice.Replace(body, candidates, replacements) }, true
}

// rewritableZones is the single definition of Gemini's rewrite zones, shared by
// ExtractCompressible (live only) and ExtractStabilizable (live + frozen), so the
// two can never disagree about which turn is the live one. Candidates come back in
// the ascending, non-overlapping document order the splicer requires: contents and
// parts are walked in order, and each turn's candidates are sorted because the
// functionResponse fields are probed by name rather than in document order.
//
// liveOnly is a pure cost switch for ExtractCompressible, which throws every frozen
// candidate away: it skips COLLECTING them, so a long conversation never pays to
// decode history the caller has already decided to discard. The live-zone RULE is
// unchanged — latestUser is still found by the same full scan of roles — so the live
// candidates, their order, and the ok result are byte-for-byte what the full walk
// produces. Collecting an earlier turn's candidates can never change ok (per-turn
// parse problems are skipped, not failed), which is what makes the skip safe.
func rewritableZones(body []byte, meta providers.RequestMetadata, liveOnly bool) ([]zoneCandidate, bool) {
	if strings.Contains(strings.ToLower(meta.Endpoint), "counttokens") {
		return nil, false
	}
	root, ok := jsonsplice.Root(body)
	if !ok {
		return nil, false
	}
	contents, ok := jsonsplice.Field(body, root, "contents")
	if !ok {
		return nil, false
	}
	items, ok := jsonsplice.Elements(body, contents)
	if !ok {
		return nil, false
	}
	latestUser := -1
	for i, item := range items {
		role, _ := jsonsplice.StringField(body, item, "role")
		if role == "user" {
			latestUser = i
		}
	}
	if latestUser < 0 {
		return nil, false
	}
	var out []zoneCandidate
	for i, item := range items {
		if liveOnly && i != latestUser {
			continue
		}
		role, _ := jsonsplice.StringField(body, item, "role")
		if role != "user" {
			continue
		}
		for _, candidate := range collectUserCandidates(body, item) {
			out = append(out, zoneCandidate{
				Candidate: candidate.Candidate,
				live:      i == latestUser,
				kind:      candidate.kind,
			})
		}
	}
	return out, true
}

func collectUserCandidates(body []byte, item jsonsplice.Span) []typedCandidate {
	partsSpan, ok := jsonsplice.Field(body, item, "parts")
	if !ok {
		return nil
	}
	parts, ok := jsonsplice.Elements(body, partsSpan)
	if !ok {
		return nil
	}
	var candidates []typedCandidate
	for _, part := range parts {
		if text, found := jsonsplice.Field(body, part, "text"); found {
			appendCandidate(body, text, "history", &candidates)
		}
		response, found := jsonsplice.Field(body, part, "functionResponse")
		if !found {
			continue
		}
		// Recovered bytes are never a compression candidate — see
		// providers.IsRecoveryToolName. Gemini names the tool on the response
		// itself, so no call-id mapping is needed.
		if nameSpan, found := jsonsplice.Field(body, response, "name"); found {
			if name, ok := jsonsplice.String(body, nameSpan); ok && providers.IsRecoveryToolName(name) {
				continue
			}
		}
		payload, found := jsonsplice.Field(body, response, "response")
		if !found {
			continue
		}
		for _, field := range []string{"output", "result", "content"} {
			if value, found := jsonsplice.Field(body, payload, field); found {
				appendCandidate(body, value, "tool_result", &candidates)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Candidate.Start < candidates[j].Candidate.Start
	})
	return candidates
}

func appendCandidate(body []byte, span jsonsplice.Span, kind string, candidates *[]typedCandidate) {
	value, ok := jsonsplice.String(body, span)
	if !ok || len(value) < minCompressBlockBytes {
		return
	}
	*candidates = append(*candidates, typedCandidate{
		Candidate: jsonsplice.Candidate{Span: span, Original: []byte(value)},
		kind:      kind,
	})
}
