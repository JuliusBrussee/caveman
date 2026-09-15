package openaicompat

import (
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// anthropicMessagesBody carries one cache_control breakpoint on the first turn,
// then an assistant reply, then TWO trailing user turns that sit past the last
// breakpoint. Under the Anthropic zone grammar both trailing user messages are
// live (content past the last breakpoint is uncached upstream, so compressing it
// cannot bust a prefix). The OpenAI grammar ignores cache_control and marks only
// the latest message live, which is exactly the misjudgment a dialect mount must
// not make: it would leave the second-to-last turn frozen and uncompressible, or
// worse rewrite a block the provider already cached.
func anthropicMessagesBody() []byte {
	frozen := strings.Repeat("FROZEN_TURN ", 60)
	mid := strings.Repeat("MID_TURN ", 60)
	live := strings.Repeat("LIVE_TURN ", 60)
	return []byte(`{"model":"glm-5.3","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"` + frozen + `","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"ok"}]},` +
		`{"role":"user","content":[{"type":"text","text":"` + mid + `"}]},` +
		`{"role":"user","content":[{"type":"text","text":"` + live + `"}]}]}`)
}

// TestNamedMountWireDialectUsesAnthropicZones pins the delegation contract: on
// an Anthropic-protocol path, a wire-dialect mount's zones equal the Anthropic
// adapter's zones for the same body, with the mid turn LIVE (the case the OpenAI
// grammar gets wrong), and the breakpoint-marked early turn frozen.
func TestNamedMountWireDialectUsesAnthropicZones(t *testing.T) {
	body := anthropicMessagesBody()
	adapter, err := NewNamedWithWireDialect("zai", "https://api.z.ai/api/anthropic", "anthropic")
	if err != nil {
		t.Fatalf("NewNamedWithWireDialect: %v", err)
	}
	meta := providers.RequestMetadata{Endpoint: "/compat/zai/v1/messages"}
	stabilizer, ok := adapter.(prefixStabilizer)
	if !ok {
		t.Fatal("named adapter must expose ExtractStabilizable")
	}
	blocks, _, ok := stabilizer.ExtractStabilizable(body, meta)
	if !ok {
		t.Fatal("ExtractStabilizable ok=false, want anthropic zones")
	}
	want, _, wantOK := (anthropic.Adapter{}).ExtractStabilizable(body, meta)
	if !wantOK || len(want) == 0 {
		t.Fatalf("anthropic reference extractor returned ok=%v blocks=%d", wantOK, len(want))
	}
	if len(blocks) != len(want) {
		t.Fatalf("dialect mount yielded %d blocks, anthropic grammar yields %d; delegation broken", len(blocks), len(want))
	}
	for i := range blocks {
		if blocks[i].Live != want[i].Live || string(blocks[i].Content) != string(want[i].Content) {
			t.Fatalf("block %d = {live:%v content:%q}, want {live:%v content:%q}", i, blocks[i].Live, blocks[i].Content, want[i].Live, want[i].Content)
		}
	}
	// The behavioral anchor that separates the grammars: the mid turn sits past
	// the last breakpoint, so it must be LIVE, not a frozen block the OpenAI
	// latest-message rule would produce.
	liveCount := 0
	for _, b := range blocks {
		if b.Live {
			liveCount++
		}
	}
	if liveCount < 2 {
		t.Fatalf("live blocks = %d, want both post-breakpoint user turns live (anthropic grammar)", liveCount)
	}

	segments, _, segOK := adapter.ExtractCompressible(body, meta)
	refSegs, _, refOK := (anthropic.Adapter{}).ExtractCompressible(body, meta)
	if segOK != refOK || len(segments) != len(refSegs) {
		t.Fatalf("ExtractCompressible segments=%d ok=%v, anthropic reference=%d ok=%v", len(segments), segOK, len(refSegs), refOK)
	}
	for i := range segments {
		if string(segments[i]) != string(refSegs[i]) {
			t.Fatalf("compressible segment %d differs from anthropic reference", i)
		}
	}
}

// TestNamedMountWireDialectKeepsOpenAIZonesOnOpenAIPaths pins the per-path rule:
// the same dialect mount serving an OpenAI-protocol path still uses the OpenAI
// wire grammar, so one mount can carry both protocols without one zone rule
// misjudging the other's bodies.
func TestNamedMountWireDialectKeepsOpenAIZonesOnOpenAIPaths(t *testing.T) {
	older := strings.Repeat("OPENAI_OLDER ", 60)
	live := strings.Repeat("OPENAI_LIVE ", 60)
	body := []byte(`{"model":"x","messages":[` +
		`{"role":"user","content":"` + older + `"},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":"` + live + `"}]}`)
	adapter, err := NewNamedWithWireDialect("zai", "https://api.example.test", "anthropic")
	if err != nil {
		t.Fatalf("NewNamedWithWireDialect: %v", err)
	}
	meta := providers.RequestMetadata{Endpoint: "/compat/zai/v1/chat/completions"}
	stabilizer := adapter.(prefixStabilizer)
	blocks, _, ok := stabilizer.ExtractStabilizable(body, meta)
	want, _, wantOK := openai.ExtractStabilizable(body, meta)
	if !ok || !wantOK || len(blocks) != len(want) {
		t.Fatalf("openai-path blocks=%d ok=%v, reference=%d ok=%v", len(blocks), ok, len(want), wantOK)
	}
	for i := range blocks {
		if blocks[i].Live != want[i].Live || string(blocks[i].Content) != string(want[i].Content) {
			t.Fatalf("openai-path block %d differs from openai reference", i)
		}
	}
}

// TestNamedMountWithoutDialectKeepsOpenAIZonesOnMessagesPath pins the default:
// a dialect-less mount uses the OpenAI grammar even on /v1/messages, exactly as
// before the wire-dialect option existed.
func TestNamedMountWithoutDialectKeepsOpenAIZonesOnMessagesPath(t *testing.T) {
	body := anthropicMessagesBody()
	adapter, err := NewNamed("zai", "https://api.example.test")
	if err != nil {
		t.Fatalf("NewNamed: %v", err)
	}
	meta := providers.RequestMetadata{Endpoint: "/compat/zai/v1/messages"}
	stabilizer := adapter.(prefixStabilizer)
	blocks, _, ok := stabilizer.ExtractStabilizable(body, meta)
	want, _, wantOK := openai.ExtractStabilizable(body, meta)
	if !ok != !wantOK && ok != wantOK {
		t.Fatalf("ok=%v, openai reference ok=%v", ok, wantOK)
	}
	if len(blocks) != len(want) {
		t.Fatalf("dialect-less blocks=%d, openai reference=%d; default grammar changed", len(blocks), len(want))
	}
	for i := range blocks {
		if blocks[i].Live != want[i].Live {
			t.Fatalf("dialect-less block %d live=%v, openai reference live=%v", i, blocks[i].Live, want[i].Live)
		}
	}
}

// TestAnthropicWireZonesAgreesWithHeaderPathRule pins the two path questions to
// one rule. SanitizeAndMapHeaders asks anthropicMessagesPath whether a request
// is Anthropic-protocol (x-api-key + anthropic-version rather than Bearer); the
// zone selector asks the same question to pick the grammar. They must never
// disagree: a mount that sends Anthropic auth headers while compressing with
// the OpenAI grammar rewrites blocks the provider cached under the other
// protocol's rules. This fails if a future edit gives either side its own copy
// of the path set.
func TestAnthropicWireZonesAgreesWithHeaderPathRule(t *testing.T) {
	built, err := NewNamedWithWireDialect("zai", "https://api.example.test", "anthropic")
	if err != nil {
		t.Fatalf("NewNamedWithWireDialect: %v", err)
	}
	adapter, ok := built.(namedAdapter)
	if !ok {
		t.Fatalf("named mount is %T, want namedAdapter", built)
	}
	paths := []string{
		"/compat/zai/v1/messages",
		"/compat/zai/v1/messages/count_tokens",
		"/compat/zai/v1/messages/",
		"/compat/zai/v1/chat/completions",
		"/compat/zai/v1/responses",
		"/compat/zai/v1/messagesx",
		"/compat/zai/v1/embeddings",
	}
	for _, path := range paths {
		if got, want := adapter.anthropicWireZones(path), adapter.anthropicMessagesPath(path); got != want {
			t.Errorf("%s: zone grammar says anthropic=%v, header mapping says anthropic=%v", path, got, want)
		}
	}
}
