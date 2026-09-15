package openai

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestExtractCompressible_LatestToolAndLatestUserOnly(t *testing.T) {
	oldUser := strings.Repeat("OLD_USER ", 80)
	oldTool := strings.Repeat("OLD_TOOL ", 80)
	liveTool := strings.Repeat("LIVE_TOOL ", 80)
	liveUser := strings.Repeat("LIVE_USER ", 80)
	body := []byte(`{
  "model":"gpt-5.5",
  "messages":[
    {"role":"system","content":"system prefix ` + oldUser + `"},
    {"role":"user","content":"` + oldUser + `"},
    {"role":"tool","tool_call_id":"old","content":"` + oldTool + `"},
    {"role":"assistant","content":"assistant middle ` + liveUser + `"},
    {"role":"tool","tool_call_id":"new","content":"` + liveTool + `"},
    {"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}},{"type":"text","text":"` + liveUser + `"}]}
  ]
}`)
	segs, reassemble, ok := New("https://api.openai.com").ExtractCompressible(body, providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions"})
	if !ok {
		t.Fatal("ExtractCompressible ok=false")
	}
	want := []string{liveTool, liveUser}
	if len(segs) != len(want) {
		t.Fatalf("segments = %d, want %d: %q", len(segs), len(want), segs)
	}
	for i := range want {
		if string(segs[i]) != want[i] {
			t.Fatalf("segment %d = %q, want %q", i, string(segs[i]), want[i])
		}
	}

	out, err := reassemble([][]byte{[]byte("TOOL_COMPRESSED"), []byte("USER_COMPRESSED")})
	if err != nil {
		t.Fatalf("reassemble: %v", err)
	}
	for _, untouched := range []string{oldUser, oldTool, "assistant middle", `"image_url"`} {
		if !bytes.Contains(out, []byte(untouched)) {
			t.Fatalf("untouched prefix/non-text content missing %q in %s", untouched, out)
		}
	}
	if bytes.Contains(out, []byte(liveTool)) || bytes.Contains(out, []byte(`"text":"`+liveUser+`"`)) {
		t.Fatalf("live tool/user text was not replaced: %s", out)
	}
}

func TestExtractCompressible_SplicePreservesPrefixSuffix(t *testing.T) {
	live := strings.Repeat("LIVE ", 120)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"system","content":"sys"},{"role":"user","content":"` + live + `"}],"temperature":0}`)
	segs, reassemble, ok := New("https://api.openai.com").ExtractCompressible(body, providers.RequestMetadata{Endpoint: "/v1/chat/completions"})
	if !ok || len(segs) != 1 {
		t.Fatalf("segments=%d ok=%v, want one", len(segs), ok)
	}
	out, err := reassemble([][]byte{[]byte("SHORT")})
	if err != nil {
		t.Fatalf("reassemble: %v", err)
	}
	first := bytes.Index(body, []byte(live))
	outFirst := bytes.Index(out, []byte("SHORT"))
	if first < 0 || outFirst < 0 {
		t.Fatal("missing splice anchors")
	}
	if sha256.Sum256(body[:first]) != sha256.Sum256(out[:outFirst]) {
		t.Fatal("prefix changed")
	}
	if sha256.Sum256(body[first+len(live):]) != sha256.Sum256(out[outFirst+len("SHORT"):]) {
		t.Fatal("suffix changed")
	}
}

func TestExtractCompressible_ThresholdAndEmbeddingsOptOut(t *testing.T) {
	small := strings.Repeat("small ", 40)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"` + small + `"}]}`)
	if _, _, ok := New("https://api.openai.com").ExtractCompressible(body, providers.RequestMetadata{Endpoint: "/v1/chat/completions"}); ok {
		t.Fatal("sub-512-byte live block must opt out")
	}
	embed := []byte(`{"model":"text-embedding-3-large","input":"` + strings.Repeat("embed ", 120) + `"}`)
	if _, _, ok := New("https://api.openai.com").ExtractCompressible(embed, providers.RequestMetadata{Endpoint: "/v1/embeddings"}); ok {
		t.Fatal("embeddings request must opt out")
	}
}

func TestExtractCompressible_ForcedTOONBypassesThresholdForToolMessagesOnly(t *testing.T) {
	t.Setenv("CAVE_ENGINE_TOON", "best-of")
	userJSON := `{"rows":[{"id":1,"name":"user"}]}`
	toolJSON := `{"rows":[{"id":1,"name":"ana","city":"boulder"},{"id":2,"name":"luis","city":"denver"}]}`
	userQuoted, _ := json.Marshal(userJSON)
	toolQuoted, _ := json.Marshal(toolJSON)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"tool","tool_call_id":"call_1","content":` + string(toolQuoted) + `},{"role":"user","content":` + string(userQuoted) + `}]}`)

	segs, _, ok := New("https://api.openai.com").ExtractCompressible(body, providers.RequestMetadata{Endpoint: "/v1/chat/completions"})
	if !ok || len(segs) != 1 || string(segs[0]) != toolJSON {
		t.Fatalf("forced TOON should admit only sub-threshold tool JSON, got %q ok=%v", segs, ok)
	}
}

func TestExtractCompressible_MalformedOptsOut(t *testing.T) {
	if _, _, ok := New("https://api.openai.com").ExtractCompressible([]byte("not json"), providers.RequestMetadata{Endpoint: "/v1/chat/completions"}); ok {
		t.Fatal("malformed JSON must opt out")
	}
}

func TestExtractCompressible_ResponsesStringInput(t *testing.T) {
	live := strings.Repeat("RESPONSES_LIVE ", 60)
	body := []byte(` { "model":"gpt-5.5", "input":` + string(mustJSON(t, live)) + `, "temperature":0 } `)
	segments, reassemble, ok := New("https://api.openai.com").ExtractCompressible(body, providers.RequestMetadata{Endpoint: "/v1/responses"})
	if !ok || len(segments) != 1 || string(segments[0]) != live {
		t.Fatalf("segments=%q ok=%v", segments, ok)
	}
	out, err := reassemble([][]byte{[]byte("SHORT")})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte(` { "model":"gpt-5.5", "input":`)) || !bytes.HasSuffix(out, []byte(`, "temperature":0 } `)) || !json.Valid(bytes.TrimSpace(out)) {
		t.Fatalf("splice changed envelope: %s", out)
	}
}

func TestExtractCompressible_ResponsesLatestUserAndToolOutputOnly(t *testing.T) {
	old := strings.Repeat("OLD ", 150)
	tool := strings.Repeat("TOOL_OUTPUT ", 60)
	user := strings.Repeat("LATEST_USER ", 60)
	body := []byte(`{"model":"gpt-5.5","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":` + string(mustJSON(t, old)) + `}]},` +
		`{"type":"function_call","name":"danger","arguments":` + string(mustJSON(t, strings.Repeat("ARG ", 200))) + `},` +
		`{"type":"function_call_output","call_id":"c1","output":` + string(mustJSON(t, tool)) + `},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + string(mustJSON(t, old)) + `}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":` + string(mustJSON(t, user)) + `}]}` +
		`]}`)
	segments, reassemble, ok := New("https://api.openai.com").ExtractCompressible(body, providers.RequestMetadata{Endpoint: "/v1/responses"})
	if !ok || len(segments) != 2 || string(segments[0]) != tool || string(segments[1]) != user {
		t.Fatalf("segments=%q ok=%v", segments, ok)
	}
	out, err := reassemble([][]byte{[]byte("TOOL_SHORT"), []byte("USER_SHORT")})
	if err != nil || !json.Valid(out) {
		t.Fatalf("reassemble=%v body=%s", err, out)
	}
	if !bytes.Contains(out, []byte(`"arguments"`)) || bytes.Count(out, []byte(old)) != 2 {
		t.Fatalf("immutable/function-call content changed: %s", out)
	}
}

func TestExtractCompressible_ResponsesCustomToolOutputAndRecovery(t *testing.T) {
	tool := strings.Repeat("CUSTOM_TOOL_OUTPUT ", 50)
	recovered := strings.Repeat("RECOVERED_BYTES ", 50)
	body := []byte(`{"model":"gpt-5.5","input":[` +
		`{"type":"custom_tool_call","call_id":"recover","name":"mcp__caveman__retrieve","input":"{}"},` +
		`{"type":"custom_tool_call_output","call_id":"recover","output":` + string(mustJSON(t, recovered)) + `},` +
		`{"type":"custom_tool_call","call_id":"normal","name":"shell","input":"{}"},` +
		`{"type":"custom_tool_call_output","call_id":"normal","output":` + string(mustJSON(t, tool)) + `}` +
		`]}`)
	segments, reassemble, ok := New("https://api.openai.com").ExtractCompressible(body, providers.RequestMetadata{Endpoint: "/v1/responses"})
	if !ok || len(segments) != 1 || string(segments[0]) != tool {
		t.Fatalf("segments=%q ok=%v", segments, ok)
	}
	out, err := reassemble([][]byte{[]byte("CUSTOM_SHORT")})
	if err != nil || !json.Valid(out) {
		t.Fatalf("reassemble=%v body=%s", err, out)
	}
	if !bytes.Contains(out, []byte(recovered)) {
		t.Fatalf("recovery output changed: %s", out)
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// ── live-zone cost ────────────────────────────────────────────────────────────
//
// ExtractCompressible discards every frozen block, so walking the history to
// DECODE those blocks is pure waste on the gateway's hot path (the managed
// gateway only ever calls ExtractCompressible). These guards pin that the
// live-only extraction stays proportional to the live turn, not to the length of
// the conversation — a regression here silently doubles the cost of every
// compressible request.

// benchChatBody renders a chat conversation with historyTurns user/assistant/tool
// triples of blockBytes each, plus one live tool + live user message.
func benchChatBody(historyTurns, blockBytes int) []byte {
	block := strings.Repeat("h", blockBytes)
	var b strings.Builder
	b.WriteString(`{"model":"gpt-5.5","messages":[{"role":"system","content":"you are a helpful assistant"}`)
	for i := 0; i < historyTurns; i++ {
		b.WriteString(`,{"role":"user","content":"` + block + `"}`)
		b.WriteString(`,{"role":"assistant","content":"ack"}`)
		b.WriteString(`,{"role":"tool","tool_call_id":"t","content":"` + block + `"}`)
	}
	b.WriteString(`,{"role":"tool","tool_call_id":"live","content":"` + block + `"}`)
	b.WriteString(`,{"role":"user","content":"` + block + `"}]}`)
	return []byte(b.String())
}

// benchResponsesBody is the same shape in the responses grammar.
func benchResponsesBody(historyTurns, blockBytes int) []byte {
	block := strings.Repeat("h", blockBytes)
	var b strings.Builder
	b.WriteString(`{"model":"gpt-5.5","input":[`)
	for i := 0; i < historyTurns; i++ {
		b.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + block + `"}]},`)
		b.WriteString(`{"type":"function_call_output","call_id":"c","output":"` + block + `"},`)
	}
	b.WriteString(`{"type":"function_call_output","call_id":"live","output":"` + block + `"},`)
	b.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + block + `"}]}]}`)
	return []byte(b.String())
}

func allocBytes(runs int, f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		f()
	}
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestExtractCompressibleDoesNotDecodeFrozenHistory is the regression guard:
// live-only extraction must not pay for the frozen blocks it throws away. With 64
// historical turns of 8 KiB, decoding them costs well over an order of magnitude
// more than the two live blocks — so a quarter of the stabilizable cost is a
// generous ceiling that still fails loudly if the frozen walk comes back.
func TestExtractCompressibleDoesNotDecodeFrozenHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
		meta providers.RequestMetadata
	}{
		{"chat", benchChatBody(64, 8192), providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions"}},
		{"responses", benchResponsesBody(64, 8192), providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/responses"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := openaiAdapter()
			live := allocBytes(20, func() {
				if _, _, ok := adapter.ExtractCompressible(tc.body, tc.meta); !ok {
					t.Fatal("ExtractCompressible opted out")
				}
			})
			all := allocBytes(20, func() {
				if _, _, ok := adapter.ExtractStabilizable(tc.body, tc.meta); !ok {
					t.Fatal("ExtractStabilizable opted out")
				}
			})
			if live*4 >= all {
				t.Fatalf("ExtractCompressible allocated %d bytes vs ExtractStabilizable %d — the live-only path is decoding frozen history it discards", live, all)
			}
		})
	}
}

func BenchmarkExtractCompressibleChat(b *testing.B) {
	adapter := New("https://api.openai.com")
	body := benchChatBody(64, 8192)
	meta := providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, ok := adapter.ExtractCompressible(body, meta); !ok {
			b.Fatal("opted out")
		}
	}
}

func BenchmarkExtractStabilizableChat(b *testing.B) {
	adapter := openaiAdapter()
	body := benchChatBody(64, 8192)
	meta := providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, ok := adapter.ExtractStabilizable(body, meta); !ok {
			b.Fatal("opted out")
		}
	}
}

func BenchmarkExtractCompressibleResponses(b *testing.B) {
	adapter := New("https://api.openai.com")
	body := benchResponsesBody(64, 8192)
	meta := providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/responses"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, ok := adapter.ExtractCompressible(body, meta); !ok {
			b.Fatal("opted out")
		}
	}
}
