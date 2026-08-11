package compressors

import (
	"github.com/JuliusBrussee/caveman/engine/safety"
	"github.com/JuliusBrussee/caveman/engine/tokens"
)

type jsonStrategy struct {
	toon    *toonCompressor
	elision *jsonCompressor
	counter tokens.Counter
}

// NewJSONStrategy returns the transparent JSON best-of selector. It is meant to
// replace NewJSON() for content type "json" only when the engine's TOON flag is
// set to best-of.
func NewJSONStrategy(counter tokens.Counter) Compressor {
	if counter == nil {
		counter = tokens.Default()
	}
	return &jsonStrategy{
		toon:    &toonCompressor{opt: EncodeOptions{Delimiter: ','}},
		elision: &jsonCompressor{arrayThreshold: 8, keepFirst: 3, keepLast: 2, maxItems: 50, anomalyZ: 2.0, relevanceMin: 0.30},
		counter: counter,
	}
}

func (s *jsonStrategy) ContentType() string       { return "json" }
func (s *jsonStrategy) SafetyClass() safety.Class { return safety.S4 }
func (s *jsonStrategy) Compress(input []byte) ([]byte, bool) {
	out, _, ok := s.CompressWithMetadata(input, "")
	return out, ok
}

func (s *jsonStrategy) CompressQuery(input []byte, query string) ([]byte, bool) {
	out, _, ok := s.CompressWithMetadata(input, query)
	return out, ok
}

func (s *jsonStrategy) CompressWithMetadata(input []byte, query string) ([]byte, Metadata, bool) {
	before := s.counter.Count(input)
	var best []byte
	var bestAfter int
	var bestMeta Metadata

	if out, ok := s.toon.Compress(input); ok {
		after := s.counter.Count(out)
		if after < before {
			best = out
			bestAfter = after
			bestMeta = Metadata{Method: "toon", LosslessToModel: metadataBool(true)}
		}
	}

	var elided []byte
	var ok bool
	if query != "" {
		elided, ok = s.elision.CompressQuery(input, query)
	} else {
		elided, ok = s.elision.Compress(input)
	}
	if ok {
		after := s.counter.Count(elided)
		if after < before && (best == nil || after < bestAfter) {
			best = elided
			bestAfter = after
			bestMeta = Metadata{Method: "elision", LosslessToModel: metadataBool(false)}
		}
	}

	if best == nil {
		return nil, Metadata{}, false
	}
	return best, bestMeta, true
}
