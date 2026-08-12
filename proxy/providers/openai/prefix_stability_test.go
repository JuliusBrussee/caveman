package openai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// Zone tagging for the cross-turn prefix fix. OpenAI caches long prompt prefixes
// automatically, so a message this proxy compressed while it was the live zone must
// keep going upstream as those SAME bytes once it drops into the history — which
// means the adapter has to expose the history blocks too, marked frozen so they can
// only ever be substituted, never newly compressed.

func openaiAdapter() Adapter { return New("https://api.openai.com").(Adapter) }

func chatMeta() providers.RequestMetadata {
	return providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions"}
}

// TestExtractStabilizable_ChatTagsFrozenAndLiveBlocks pins the chat grammar's zones:
// the latest tool and latest user message are Live, every EARLIER user/tool message
// is frozen (substitute-only), and system/assistant messages are never exposed at
// all because the proxy never compresses them.
func TestExtractStabilizable_ChatTagsFrozenAndLiveBlocks(t *testing.T) {
	oldUser := strings.Repeat("OLD_USER ", 80)
	oldTool := strings.Repeat("OLD_TOOL ", 80)
	system := strings.Repeat("SYSTEM ", 80)
	assistant := strings.Repeat("ASSISTANT ", 80)
	liveTool := strings.Repeat("LIVE_TOOL ", 80)
	liveUser := strings.Repeat("LIVE_USER ", 80)
	body := []byte(`{"model":"gpt-5.5","messages":[` +
		`{"role":"system","content":"` + system + `"},` +
		`{"role":"user","content":"` + oldUser + `"},` +
		`{"role":"tool","tool_call_id":"old","content":"` + oldTool + `"},` +
		`{"role":"assistant","content":"` + assistant + `"},` +
		`{"role":"tool","tool_call_id":"new","content":"` + liveTool + `"},` +
		`{"role":"user","content":[{"type":"text","text":"` + liveUser + `"}]}` +
		`]}`)

	blocks, reassemble, ok := openaiAdapter().ExtractStabilizable(body, chatMeta())
	if !ok {
		t.Fatal("ExtractStabilizable ok=false")
	}
	want := []struct {
		text string
		live bool
		kind string
	}{
		{oldUser, false, "history"},
		{oldTool, false, "tool_result"},
		{liveTool, true, "tool_result"},
		{liveUser, true, "history"},
	}
	if len(blocks) != len(want) {
		t.Fatalf("blocks = %d, want %d", len(blocks), len(want))
	}
	for i, w := range want {
		if string(blocks[i].Content) != w.text || blocks[i].Live != w.live || blocks[i].Kind != w.kind {
			t.Fatalf("block %d = live:%v %q..., want live:%v %q...", i, blocks[i].Live, string(blocks[i].Content)[:10], w.live, w.text[:10])
		}
	}

	// The reassembler still byte-splices: substituting one frozen block and leaving
	// the rest nil must change only that block's bytes.
	out, err := reassemble([][]byte{[]byte("SUBSTITUTED"), nil, nil, nil})
	if err != nil {
		t.Fatalf("reassemble: %v", err)
	}
	if !bytes.Contains(out, []byte(`"content":"SUBSTITUTED"`)) {
		t.Fatalf("frozen block was not substituted: %s", out)
	}
	for _, untouched := range []string{system, oldTool, assistant, liveTool, liveUser} {
		if !bytes.Contains(out, []byte(untouched)) {
			t.Fatalf("untouched block changed: %s", out)
		}
	}
	if !json.Valid(out) {
		t.Fatalf("splice produced invalid JSON: %s", out)
	}
}

// TestExtractStabilizable_ResponsesTagsFrozenAndLiveBlocks runs the same rule over
// the Responses grammar: the latest function_call_output and the latest user item
// are Live, earlier ones frozen, assistant items never exposed.
func TestExtractStabilizable_ResponsesTagsFrozenAndLiveBlocks(t *testing.T) {
	oldUser := strings.Repeat("OLD_USER ", 80)
	oldOutput := strings.Repeat("OLD_OUTPUT ", 80)
	assistant := strings.Repeat("ASSISTANT ", 80)
	liveOutput := strings.Repeat("LIVE_OUTPUT ", 80)
	liveUser := strings.Repeat("LIVE_USER ", 80)
	body := []byte(`{"model":"gpt-5.5","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + oldUser + `"}]},` +
		`{"type":"function_call_output","call_id":"c1","output":"` + oldOutput + `"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + assistant + `"}]},` +
		`{"type":"function_call_output","call_id":"c2","output":"` + liveOutput + `"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + liveUser + `"}]}` +
		`]}`)

	blocks, _, ok := openaiAdapter().ExtractStabilizable(body, providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/responses"})
	if !ok {
		t.Fatal("ExtractStabilizable ok=false")
	}
	want := []struct {
		text string
		live bool
		kind string
	}{
		{oldUser, false, "history"},
		{oldOutput, false, "tool_result"},
		{liveOutput, true, "tool_result"},
		{liveUser, true, "history"},
	}
	if len(blocks) != len(want) {
		t.Fatalf("blocks = %d, want %d", len(blocks), len(want))
	}
	for i, w := range want {
		if string(blocks[i].Content) != w.text || blocks[i].Live != w.live || blocks[i].Kind != w.kind {
			t.Fatalf("block %d = live:%v %q..., want live:%v %q...", i, blocks[i].Live, string(blocks[i].Content)[:10], w.live, w.text[:10])
		}
	}
}

// TestExtractStabilizable_MatchesExtractCompressibleLiveZone is the no-drift pin:
// both extractors read the same zone definition, so the Live blocks of one must be
// exactly the segments of the other, in the same order.
func TestExtractStabilizable_MatchesExtractCompressibleLiveZone(t *testing.T) {
	for name, tc := range map[string]struct {
		body     string
		endpoint string
	}{
		"chat": {
			body: `{"model":"gpt-5.5","messages":[` +
				`{"role":"user","content":"` + strings.Repeat("A ", 400) + `"},` +
				`{"role":"assistant","content":"` + strings.Repeat("B ", 400) + `"},` +
				`{"role":"tool","tool_call_id":"t","content":"` + strings.Repeat("C ", 400) + `"},` +
				`{"role":"user","content":"` + strings.Repeat("D ", 400) + `"}]}`,
			endpoint: "/v1/chat/completions",
		},
		"responses": {
			body: `{"model":"gpt-5.5","input":[` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("A ", 400) + `"}]},` +
				`{"type":"function_call_output","call_id":"c1","output":"` + strings.Repeat("B ", 400) + `"},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("C ", 400) + `"}]}]}`,
			endpoint: "/v1/responses",
		},
		"responses string input": {
			body:     `{"model":"gpt-5.5","input":"` + strings.Repeat("A ", 400) + `"}`,
			endpoint: "/v1/responses",
		},
	} {
		t.Run(name, func(t *testing.T) {
			meta := providers.RequestMetadata{Provider: "openai", Endpoint: tc.endpoint}
			segments, _, segOK := openaiAdapter().ExtractCompressible([]byte(tc.body), meta)
			blocks, _, blockOK := openaiAdapter().ExtractStabilizable([]byte(tc.body), meta)
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
		})
	}
}

// TestExtractStabilizable_OptsOutWhereCompressionDoes keeps the carve-outs aligned:
// an endpoint whose bytes must be preserved exposes no blocks at all, so no
// substitution can reach it either.
func TestExtractStabilizable_OptsOutWhereCompressionDoes(t *testing.T) {
	large := strings.Repeat("X ", 400)
	cases := map[string]struct {
		body     string
		endpoint string
	}{
		"embeddings":      {`{"model":"text-embedding-3-large","input":"` + large + `"}`, "/v1/embeddings"},
		"malformed":       {`not json`, "/v1/chat/completions"},
		"no messages":     {`{"model":"gpt-5.5"}`, "/v1/chat/completions"},
		"assistant only":  {`{"model":"gpt-5.5","messages":[{"role":"assistant","content":"` + large + `"}]}`, "/v1/chat/completions"},
		"below threshold": {`{"model":"gpt-5.5","messages":[{"role":"user","content":"tiny"}]}`, "/v1/chat/completions"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := openaiAdapter().ExtractStabilizable([]byte(tc.body), providers.RequestMetadata{Provider: "openai", Endpoint: tc.endpoint}); ok {
				t.Fatal("ExtractStabilizable must opt out")
			}
		})
	}
}
