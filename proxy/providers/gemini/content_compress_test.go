package gemini

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestExtractCompressibleLatestUserTextAndFunctionResponse(t *testing.T) {
	old := strings.Repeat("OLD ", 150)
	live := strings.Repeat("LIVE ", 150)
	result := strings.Repeat("RESULT ", 100)
	args := strings.Repeat("ARG ", 200)
	body := []byte(`{"contents":[` +
		`{"role":"user","parts":[{"text":` + quote(t, old) + `}]},` +
		`{"role":"model","parts":[{"functionCall":{"name":"x","args":{"secret":` + quote(t, args) + `}}}]},` +
		`{"role":"user","parts":[{"functionResponse":{"name":"x","response":{"output":` + quote(t, result) + `}}},{"text":` + quote(t, live) + `}]}` +
		`]}`)
	segments, reassemble, ok := New("https://generativelanguage.googleapis.com").ExtractCompressible(body, providers.RequestMetadata{Endpoint: "/v1beta/models/gemini:generateContent"})
	if !ok || len(segments) != 2 || string(segments[0]) != result || string(segments[1]) != live {
		t.Fatalf("segments=%q ok=%v", segments, ok)
	}
	out, err := reassemble([][]byte{[]byte("RESULT_SHORT"), []byte("LIVE_SHORT")})
	if err != nil || !json.Valid(out) || !bytes.Contains(out, []byte(args)) || bytes.Count(out, []byte(old)) != 1 {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

func TestExtractCompressibleMalformedThresholdAndCountOptOut(t *testing.T) {
	adapter := New("https://generativelanguage.googleapis.com")
	if _, _, ok := adapter.ExtractCompressible([]byte(`{"contents":[{"role":"user","parts":[{"text":"small"}]}]}`), providers.RequestMetadata{Endpoint: "generateContent"}); ok {
		t.Fatal("small text must opt out")
	}
	if _, _, ok := adapter.ExtractCompressible([]byte(`not json`), providers.RequestMetadata{Endpoint: "generateContent"}); ok {
		t.Fatal("malformed body must opt out")
	}
	large := strings.Repeat("large ", 100)
	if _, _, ok := adapter.ExtractCompressible([]byte(`{"contents":[{"role":"user","parts":[{"text":`+quote(t, large)+`}]}]}`), providers.RequestMetadata{Endpoint: "countTokens"}); ok {
		t.Fatal("countTokens must opt out")
	}
}

func quote(t *testing.T, value string) string {
	t.Helper()
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// ── live-zone cost ────────────────────────────────────────────────────────────
//
// ExtractCompressible discards every frozen turn, so walking the history to
// DECODE those turns is pure waste on the gateway's hot path (the managed gateway
// only ever calls ExtractCompressible). These guards pin that the live-only
// extraction stays proportional to the live turn, not to the length of the
// conversation.

// benchContentsBody renders a conversation with historyTurns user/model pairs of
// blockBytes each, plus one live user turn.
func benchContentsBody(historyTurns, blockBytes int) []byte {
	block := strings.Repeat("h", blockBytes)
	var b strings.Builder
	b.WriteString(`{"contents":[`)
	for i := 0; i < historyTurns; i++ {
		b.WriteString(`{"role":"user","parts":[{"text":"` + block + `"},{"functionResponse":{"name":"x","response":{"output":"` + block + `"}}}]},`)
		b.WriteString(`{"role":"model","parts":[{"text":"ack"}]},`)
	}
	b.WriteString(`{"role":"user","parts":[{"text":"` + block + `"}]}]}`)
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
// live-only extraction must not pay for the frozen turns it throws away.
func TestExtractCompressibleDoesNotDecodeFrozenHistory(t *testing.T) {
	adapter := geminiAdapter()
	body := benchContentsBody(64, 8192)
	meta := providers.RequestMetadata{Provider: "gemini", Endpoint: "/v1beta/models/gemini-3-pro:generateContent"}
	live := allocBytes(20, func() {
		if _, _, ok := adapter.ExtractCompressible(body, meta); !ok {
			t.Fatal("ExtractCompressible opted out")
		}
	})
	all := allocBytes(20, func() {
		if _, _, ok := adapter.ExtractStabilizable(body, meta); !ok {
			t.Fatal("ExtractStabilizable opted out")
		}
	})
	if live*4 >= all {
		t.Fatalf("ExtractCompressible allocated %d bytes vs ExtractStabilizable %d — the live-only path is decoding frozen history it discards", live, all)
	}
}

func BenchmarkExtractCompressible(b *testing.B) {
	adapter := New("https://generativelanguage.googleapis.com")
	body := benchContentsBody(64, 8192)
	meta := providers.RequestMetadata{Provider: "gemini", Endpoint: "/v1beta/models/gemini-3-pro:generateContent"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, ok := adapter.ExtractCompressible(body, meta); !ok {
			b.Fatal("opted out")
		}
	}
}

func BenchmarkExtractStabilizable(b *testing.B) {
	adapter := geminiAdapter()
	body := benchContentsBody(64, 8192)
	meta := providers.RequestMetadata{Provider: "gemini", Endpoint: "/v1beta/models/gemini-3-pro:generateContent"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, ok := adapter.ExtractStabilizable(body, meta); !ok {
			b.Fatal("opted out")
		}
	}
}
