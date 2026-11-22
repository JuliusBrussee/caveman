package gateway

import (
	"crypto/sha256"
	"fmt"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/tokens"
)

const (
	// dedupOptimizerID labels live-zone duplicate tool-output collapsing in
	// x-cave-optimization and telemetry rows.
	dedupOptimizerID = "duplicate-read-collapse"

	// minDedupBlockBytes keeps small repeated tool outputs ("ok", "Exit code 0",
	// short status checks) untouched so only genuinely large duplicate reads
	// collapse.
	minDedupBlockBytes = 1024
)

// dedupBlockInfo extracts the semantically-safe duplicate identity and trailing
// `<system-reminder>` suffix for a live-zone `tool_result` block. Returns
// ok=false for non-tool blocks, standalone `<system-reminder>` blocks, or
// payloads whose real inner body (excluding `<system-reminder>` / volatile
// headers) is below `minDedupBlockBytes`.
func dedupBlockInfo(block rewritableBlock) (digest [32]byte, trailingReminder []byte, ok bool) {
	if block.kind != "tool_result" {
		return [32]byte{}, nil, false
	}
	dc := engine.ExtractDedupComponents(block.content)
	if dc.SkipDedup || dc.InnerLen < minDedupBlockBytes {
		return [32]byte{}, nil, false
	}
	return sha256.Sum256(dc.Key), dc.TrailingReminder, true
}

// buildDuplicateStub returns the compact pointer that replaces a 2nd+ identical
// live-zone tool result, carrying its exact CCR recovery handle and re-attaching
// any trailing `<system-reminder>` verbatim so live harness instructions are
// never dropped.
func buildDuplicateStub(rawLen int, handle string, trailingReminder []byte) []byte {
	body := fmt.Sprintf(
		"[caveman: duplicate tool output (%d bytes) — identical content already appears earlier in this conversation; use caveman_retrieve if verbatim bytes are needed]",
		rawLen,
	)
	stub := appendCCRMarker([]byte(body), handle)
	if len(trailingReminder) > 0 {
		out := make([]byte, 0, len(stub)+len(trailingReminder))
		out = append(out, stub...)
		out = append(out, trailingReminder...)
		return out
	}
	return stub
}

func countBlockTokens(data []byte) int {
	n := tokens.Default().Count(data)
	if n <= 0 && len(data) > 0 {
		return (len(data) + 3) / 4
	}
	return n
}
