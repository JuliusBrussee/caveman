package compressors

import "github.com/JuliusBrussee/caveman/engine/safety"

type toonCompressor struct {
	opt EncodeOptions
}

// NewTOON returns the lossless TOON re-encoder. It is reached only by forcing
// Options.Type = "toon"; Detect never routes to it.
func NewTOON() Compressor {
	return &toonCompressor{opt: EncodeOptions{Delimiter: ','}}
}

func (c *toonCompressor) ContentType() string       { return "toon" }
func (c *toonCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *toonCompressor) Compress(input []byte) ([]byte, bool) {
	v, ok := parseJSONTOON(input)
	if !ok {
		return nil, false
	}
	return encodeTOON(v, c.opt)
}

func (c *toonCompressor) CompressWithMetadata(input []byte, _ string) ([]byte, Metadata, bool) {
	out, ok := c.Compress(input)
	return out, Metadata{Method: "toon", LosslessToModel: metadataBool(true)}, ok
}
