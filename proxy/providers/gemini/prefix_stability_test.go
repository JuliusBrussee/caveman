package gemini

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// Zone tagging for the cross-turn prefix fix. Gemini caches repeated prompt
// prefixes implicitly, so a turn this proxy compressed while it was the live zone
// must keep going upstream as those SAME bytes once it drops into the history —
// which means the adapter has to expose the history turns too, marked frozen so
// they can only ever be substituted, never newly compressed.

func geminiAdapter() Adapter { return New("https://generativelanguage.googleapis.com").(Adapter) }

func generateContentMeta() providers.RequestMetadata {
	return providers.RequestMetadata{Provider: "gemini", Endpoint: "/v1beta/models/gemini-3-pro:generateContent"}
}

// TestExtractStabilizableTagsFrozenAndLiveTurns pins the zones: the latest user
// turn is Live, every earlier user turn is frozen (substitute-only), and model
// turns are never exposed at all because the proxy never compresses them.
func TestExtractStabilizableTagsFrozenAndLiveTurns(t *testing.T) {
	oldText := strings.Repeat("OLD_TEXT ", 80)
	oldResult := strings.Repeat("OLD_RESULT ", 80)
	model := strings.Repeat("MODEL ", 80)
	liveResult := strings.Repeat("LIVE_RESULT ", 80)
	liveText := strings.Repeat("LIVE_TEXT ", 80)
	body := []byte(`{"contents":[` +
		`{"role":"user","parts":[{"text":` + quote(t, oldText) + `}]},` +
		`{"role":"user","parts":[{"functionResponse":{"name":"x","response":{"output":` + quote(t, oldResult) + `}}}]},` +
		`{"role":"model","parts":[{"text":` + quote(t, model) + `}]},` +
		`{"role":"user","parts":[{"functionResponse":{"name":"y","response":{"output":` + quote(t, liveResult) + `}}},{"text":` + quote(t, liveText) + `}]}` +
		`]}`)

	blocks, reassemble, ok := geminiAdapter().ExtractStabilizable(body, generateContentMeta())
	if !ok {
		t.Fatal("ExtractStabilizable ok=false")
	}
	want := []struct {
		text string
		live bool
		kind string
	}{
		{oldText, false, "history"},
		{oldResult, false, "tool_result"},
		{liveResult, true, "tool_result"},
		{liveText, true, "history"},
	}
	if len(blocks) != len(want) {
		t.Fatalf("blocks = %d, want %d", len(blocks), len(want))
	}
	for i, w := range want {
		if string(blocks[i].Content) != w.text || blocks[i].Live != w.live || blocks[i].Kind != w.kind {
			t.Fatalf("block %d = live:%v %q..., want live:%v %q...", i, blocks[i].Live, string(blocks[i].Content)[:9], w.live, w.text[:9])
		}
	}

	// The reassembler still byte-splices: substituting one frozen block and leaving
	// the rest nil must change only that block's bytes.
	out, err := reassemble([][]byte{[]byte("SUBSTITUTED"), nil, nil, nil})
	if err != nil {
		t.Fatalf("reassemble: %v", err)
	}
	if !bytes.Contains(out, []byte(`"text":"SUBSTITUTED"`)) {
		t.Fatalf("frozen block was not substituted: %s", out)
	}
	for _, untouched := range []string{oldResult, model, liveResult, liveText} {
		if !bytes.Contains(out, []byte(untouched)) {
			t.Fatalf("untouched block changed: %s", out)
		}
	}
	if !json.Valid(out) {
		t.Fatalf("splice produced invalid JSON: %s", out)
	}
}

// TestExtractStabilizableMatchesExtractCompressibleLiveZone is the no-drift pin:
// both extractors read the same zone definition, so the Live blocks of one must be
// exactly the segments of the other, in the same order.
func TestExtractStabilizableMatchesExtractCompressibleLiveZone(t *testing.T) {
	first := strings.Repeat("FIRST ", 120)
	second := strings.Repeat("SECOND ", 120)
	third := strings.Repeat("THIRD ", 120)
	body := []byte(`{"contents":[` +
		`{"role":"user","parts":[{"text":` + quote(t, first) + `}]},` +
		`{"role":"model","parts":[{"text":` + quote(t, second) + `}]},` +
		`{"role":"user","parts":[{"text":` + quote(t, third) + `}]}` +
		`]}`)

	segments, _, segOK := geminiAdapter().ExtractCompressible(body, generateContentMeta())
	blocks, _, blockOK := geminiAdapter().ExtractStabilizable(body, generateContentMeta())
	if !segOK || !blockOK {
		t.Fatalf("ok = %v/%v, want both true", segOK, blockOK)
	}
	var live [][]byte
	for _, b := range blocks {
		if b.Live {
			live = append(live, b.Content)
		}
	}
	if len(live) != len(segments) {
		t.Fatalf("live blocks = %d, compressible segments = %d", len(live), len(segments))
	}
	for i := range segments {
		if !bytes.Equal(live[i], segments[i]) {
			t.Fatalf("live block %d diverged from the compressible segment", i)
		}
	}
}

// TestExtractStabilizableOptsOutWhereCompressionDoes keeps the carve-outs aligned:
// an endpoint or body whose bytes must be preserved exposes no blocks at all, so no
// substitution can reach it either.
func TestExtractStabilizableOptsOutWhereCompressionDoes(t *testing.T) {
	large := strings.Repeat("X ", 400)
	cases := map[string]struct {
		body     string
		endpoint string
	}{
		"countTokens":     {`{"contents":[{"role":"user","parts":[{"text":` + quote(t, large) + `}]}]}`, "/v1beta/models/gemini-3-pro:countTokens"},
		"malformed":       {`not json`, "generateContent"},
		"model only":      {`{"contents":[{"role":"model","parts":[{"text":` + quote(t, large) + `}]}]}`, "generateContent"},
		"below threshold": {`{"contents":[{"role":"user","parts":[{"text":"tiny"}]}]}`, "generateContent"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := geminiAdapter().ExtractStabilizable([]byte(tc.body), providers.RequestMetadata{Provider: "gemini", Endpoint: tc.endpoint}); ok {
				t.Fatal("ExtractStabilizable must opt out")
			}
		})
	}
}
