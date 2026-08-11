package engine

import (
	"encoding/json"
	"fmt"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

// EncodeTOON converts JSON to the lossless TOON subset without CCR or size
// gating. It is for explicit agent/CLI requests, not automatic proxy routing.
func (e *Engine) EncodeTOON(input []byte) ([]byte, error) {
	out, ok := compressors.NewTOON().Compress(input)
	if !ok {
		return nil, fmt.Errorf("not losslessly TOON-encodable (invalid JSON, or nested/non-uniform shape); keep JSON")
	}
	return out, nil
}

// DecodeTOON converts TOON back to compact JSON. Malformed TOON fails closed.
func (e *Engine) DecodeTOON(input []byte) ([]byte, error) {
	v, ok := compressors.DecodeTOON(input)
	if !ok {
		return nil, fmt.Errorf("not valid TOON")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return out, nil
}
