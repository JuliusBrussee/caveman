package anthropic

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestExtractCompressible_LatestMutableUserOnlyAndSpliceFidelity(t *testing.T) {
	old := strings.Repeat("OLD_HISTORY ", 70)
	live := strings.Repeat("LIVE_USER ", 80)
	tool := strings.Repeat("TOOL_RESULT ", 70)
	body := []byte(`{
  "model":"claude-sonnet-4-6",
  "system":[{"type":"text","text":"system prefix"}],
  "tools":[{"name":"read","description":"tool prefix","input_schema":{"type":"object"}}],
  "messages":[
    {"role":"user","content":[{"type":"text","text":"` + old + `","cache_control":{"type":"ephemeral"}}]},
    {"role":"assistant","content":[{"type":"text","text":"assistant hot"}]},
    {"role":"user","content":[
      {"type":"tool_use","id":"t1","name":"noop","input":{"q":"` + live + `"}},
      {"type":"thinking","thinking":"` + live + `"},
      {"type":"text","text":"` + live + `"},
      {"type":"tool_result","tool_use_id":"t1","content":"` + tool + `"}
    ]}
  ]
}`)
	adapter := New("https://api.anthropic.com")

	segs, reassemble, ok := adapter.ExtractCompressible(body, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"})
	if !ok {
		t.Fatal("ExtractCompressible ok=false")
	}
	want := []string{live, tool}
	if len(segs) != len(want) {
		t.Fatalf("segments = %d, want %d: %q", len(segs), len(want), segs)
	}
	for i := range want {
		if string(segs[i]) != want[i] {
			t.Fatalf("segment %d = %q, want %q", i, string(segs[i]), want[i])
		}
	}

	out, err := reassemble([][]byte{[]byte("COMPRESSED_TEXT"), nil})
	if err != nil {
		t.Fatalf("reassemble: %v", err)
	}
	if bytes.Contains(out, []byte(`"text":"`+live+`"`)) {
		t.Fatalf("live text block was not replaced: %s", out)
	}
	if !bytes.Contains(out, []byte(tool)) {
		t.Fatalf("unchanged tool_result must stay byte-identical")
	}
	if !bytes.Contains(out, []byte(old)) || !bytes.Contains(out, []byte(`"tool_use"`)) || !bytes.Contains(out, []byte(`"thinking"`)) {
		t.Fatalf("hot-zone/non-text bytes were changed or dropped: %s", out)
	}

	targetNeedle := []byte(`"text":"` + live + `"`)
	first := bytes.Index(body, targetNeedle)
	if first >= 0 {
		first += len(`"text":"`)
	}
	outFirst := bytes.Index(out, []byte("COMPRESSED_TEXT"))
	if first < 0 || outFirst < 0 {
		t.Fatalf("missing expected splice anchors")
	}
	if sha256.Sum256(body[:first]) != sha256.Sum256(out[:outFirst]) {
		t.Fatalf("prefix before first splice changed")
	}
	origSuffixStart := first + len(live)
	outSuffixStart := outFirst + len("COMPRESSED_TEXT")
	if sha256.Sum256(body[origSuffixStart:]) != sha256.Sum256(out[outSuffixStart:]) {
		t.Fatalf("suffix after splice changed")
	}
}

func TestExtractCompressible_ClaudeCodeTrailingSystemTail(t *testing.T) {
	// Older Claude Code shape (captured 2026-07-07): a marked latest user message
	// followed by a large trailing system-role message. Everything past the last
	// cache_control marker is provider-uncacheable tail — the trailing system
	// message must be compressible; the marked user message (cache-write covered)
	// and everything before it must not be.
	marked := strings.Repeat("marked user prompt ", 60)
	trailing := strings.Repeat("trailing system context ", 90)
	body := []byte(`{"model":"claude-fable-5",` +
		`"system":[{"type":"text","text":"You are Claude Code","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"preamble"},{"type":"text","text":"` + marked + `","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"system","content":"` + trailing + `"}]}`)
	segs, reassemble, ok := New("https://api.anthropic.com").ExtractCompressible(body, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"})
	if !ok || len(segs) != 1 {
		t.Fatalf("ok=%v segments=%d, want exactly the trailing system tail", ok, len(segs))
	}
	if string(segs[0]) != trailing {
		t.Fatalf("segment = %q..., want trailing system content", string(segs[0][:40]))
	}
	out, err := reassemble([][]byte{[]byte("TAIL_COMPRESSED")})
	if err != nil {
		t.Fatalf("reassemble: %v", err)
	}
	if !bytes.Contains(out, []byte(marked)) || !bytes.Contains(out, []byte("TAIL_COMPRESSED")) {
		t.Fatalf("marked user block must stay byte-identical and tail must be replaced: %s", out)
	}
	// Frozen prefix (everything before the trailing system content) byte-equal.
	cut := bytes.Index(body, []byte(trailing))
	if cut < 0 || !bytes.Equal(body[:cut], out[:cut]) {
		t.Fatalf("bytes before the uncacheable tail changed")
	}

	// Assistant messages in the tail are never targeted.
	bodyAssistantTail := []byte(`{"model":"claude-fable-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"` + marked + `","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"` + trailing + `"}]}]}`)
	if _, _, ok := New("https://api.anthropic.com").ExtractCompressible(bodyAssistantTail, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}); ok {
		t.Fatalf("assistant tail must not be compressible")
	}
}

func TestExtractCompressible_ClaudeCodeMarkedSystemAfterToolResult(t *testing.T) {
	// Claude Code 2.1.220 shape captured by CaveBench on 2026-07-31: new tool
	// output is followed by a tiny system message carrying the cache breakpoint.
	// Both are part of the first cache write. Freezing at the final marker would
	// permanently skip the only large, compressible block.
	tool := strings.Repeat("2026-07-30 level=INFO service=checkout status=200\n", 1200)
	body := []byte(`{"model":"claude-sonnet-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"find failure"}]},` +
		`{"role":"system","content":"runtime metadata"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"load_case","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":` + strconv.Quote(tool) + `}]},` +
		`{"role":"system","content":[{"type":"text","text":"tool result attached","cache_control":{"type":"ephemeral"}}]}` +
		`]}`)
	segments, reassemble, ok := New("https://api.anthropic.com").ExtractCompressible(body, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"})
	if !ok || len(segments) != 1 || string(segments[0]) != tool {
		t.Fatalf("ok=%v segments=%d, want marked-system-adjacent tool result", ok, len(segments))
	}
	out, err := reassemble([][]byte{[]byte("COMPRESSED_TOOL_RESULT")})
	if err != nil {
		t.Fatalf("reassemble: %v", err)
	}
	if bytes.Contains(out, []byte(tool)) || !bytes.Contains(out, []byte("COMPRESSED_TOOL_RESULT")) {
		t.Fatalf("tool result was not replaced: %s", out)
	}
	if !bytes.Contains(out, []byte(`"cache_control":{"type":"ephemeral"}`)) {
		t.Fatalf("trailing cache marker changed: %s", out)
	}
}

func TestExtractCompressible_SpliceFidelityNonASCII(t *testing.T) {
	// Multi-byte UTF-8 + escape-heavy content before and inside the live zone:
	// splice offsets must stay byte-exact through CJK, emoji, \uXXXX and \" runs.
	old := strings.Repeat(`历史 τιμή \"quoted\" é🦴 `, 30)
	live := strings.Repeat(`ライブ 🚀 caveman says \"ug\" \\path\\to\\file 終わり `, 30)
	body := []byte(`{"model":"claude-sonnet-4-6","system":[{"type":"text","text":"système ✓"}],"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"` + old + `"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"応答 ✅"}]},` +
		`{"role":"user","content":[{"type":"text","text":"` + live + `"}]}]}`)
	adapter := New("https://api.anthropic.com")

	segs, reassemble, ok := adapter.ExtractCompressible(body, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"})
	if !ok || len(segs) != 1 {
		t.Fatalf("ok=%v segments=%d, want 1 live segment", ok, len(segs))
	}
	replacement := "圧縮 🤏 \"done\""
	out, err := reassemble([][]byte{[]byte(replacement)})
	if err != nil {
		t.Fatalf("reassemble: %v", err)
	}
	if !json.Valid(out) {
		t.Fatalf("spliced body is not valid JSON: %s", out)
	}
	var decoded struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal spliced body: %v", err)
	}
	if got := decoded.Messages[2].Content[0].Text; got != replacement {
		t.Fatalf("live text = %q, want %q", got, replacement)
	}
	// Every byte before the live-zone content string must be identical.
	needle := []byte(`"text":"` + live + `"`)
	cut := bytes.Index(body, needle)
	if cut < 0 {
		t.Fatal("live needle not found")
	}
	cut += len(`"text":"`)
	if sha256.Sum256(body[:cut]) != sha256.Sum256(out[:cut]) {
		t.Fatalf("prefix before non-ASCII splice changed")
	}
	// And the suffix after it (closing quote onward) must be identical too.
	origSuffix := body[cut+len(live):]
	if !bytes.HasSuffix(out, origSuffix) {
		t.Fatalf("suffix after non-ASCII splice changed")
	}
}

func TestComputeFrozenCount_MessageMarkersOnly(t *testing.T) {
	messages := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"a"}]}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"c"}]}`),
	}
	if got := ComputeFrozenCount(messages); got != 2 {
		t.Fatalf("floor = %d, want 2", got)
	}
	if got := ComputeFrozenCount(nil); got != 0 {
		t.Fatalf("nil floor = %d, want 0", got)
	}
	noMarkers := []json.RawMessage{json.RawMessage(`{"role":"user","content":"x"}`)}
	if got := ComputeFrozenCount(noMarkers); got != 0 {
		t.Fatalf("no-marker floor = %d, want 0", got)
	}

	// Claude Code marks the NEWEST message: the trailing message stays live.
	markedLast := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"a"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}]}`),
	}
	if got := ComputeFrozenCount(markedLast); got != 1 {
		t.Fatalf("marker-on-last floor = %d, want 1 (trailing message reserved as live zone)", got)
	}
	single := []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"text","text":"only","cache_control":{"type":"ephemeral"}}]}`)}
	if got := ComputeFrozenCount(single); got != 0 {
		t.Fatalf("single marked message floor = %d, want 0", got)
	}
	markedSystemAfterToolResult := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"prompt"}]}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"load","input":{}}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"large"}]}`),
		json.RawMessage(`{"role":"system","content":[{"type":"text","text":"tail","cache_control":{"type":"ephemeral"}}]}`),
	}
	if got := ComputeFrozenCount(markedSystemAfterToolResult); got != 2 {
		t.Fatalf("marked system after tool result floor = %d, want 2", got)
	}
	markedSystemAfterPlainUser := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"plain"}]}`),
		json.RawMessage(`{"role":"system","content":[{"type":"text","text":"tail","cache_control":{"type":"ephemeral"}}]}`),
	}
	if got := ComputeFrozenCount(markedSystemAfterPlainUser); got != 1 {
		t.Fatalf("marked system after plain user floor = %d, want 1", got)
	}

	bodyWithSystemAndToolsMarkers := []byte(`{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],"tools":[{"name":"t","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("mutable ", 80) + `"}]}]}`)
	segs, _, ok := New("https://api.anthropic.com").ExtractCompressible(bodyWithSystemAndToolsMarkers, providers.RequestMetadata{Endpoint: "/v1/messages"})
	if !ok || len(segs) != 1 {
		t.Fatalf("system/tools cache_control must not freeze messages, ok=%v segments=%d", ok, len(segs))
	}
}

func TestExtractCompressible_BlockThresholdAndCountTokens(t *testing.T) {
	small := strings.Repeat("small ", 40)
	large := strings.Repeat("large ", 90)
	body := []byte(`{"model":"claude","messages":[{"role":"user","content":[{"type":"text","text":"` + small + `"},{"type":"text","text":"` + large + `"}]}]}`)
	segs, _, ok := New("https://api.anthropic.com").ExtractCompressible(body, providers.RequestMetadata{Endpoint: "/v1/messages"})
	if !ok || len(segs) != 1 || string(segs[0]) != large {
		t.Fatalf("threshold segments = %q ok=%v, want only large block", segs, ok)
	}
	if _, _, ok := New("https://api.anthropic.com").ExtractCompressible(body, providers.RequestMetadata{Endpoint: "/v1/messages/count_tokens"}); ok {
		t.Fatal("count_tokens route must opt out")
	}
}

func TestExtractCompressible_ForcedTOONBypassesThresholdForToolResultsOnly(t *testing.T) {
	t.Setenv("CAVE_ENGINE_TOON", "best-of")
	textJSON := `{"rows":[{"id":1,"name":"text"}]}`
	toolJSON := `{"rows":[{"id":1,"name":"ana","city":"boulder"},{"id":2,"name":"luis","city":"denver"}]}`
	textQuoted, _ := json.Marshal(textJSON)
	toolQuoted, _ := json.Marshal(toolJSON)
	body := []byte(`{"model":"claude","messages":[{"role":"user","content":[{"type":"text","text":` + string(textQuoted) + `},{"type":"tool_result","tool_use_id":"toolu_1","content":` + string(toolQuoted) + `}]}]}`)

	segs, _, ok := New("https://api.anthropic.com").ExtractCompressible(body, providers.RequestMetadata{Endpoint: "/v1/messages"})
	if !ok || len(segs) != 1 || string(segs[0]) != toolJSON {
		t.Fatalf("forced TOON should admit only sub-threshold tool_result JSON, got %q ok=%v", segs, ok)
	}
}

func TestExtractCompressible_MalformedOptsOut(t *testing.T) {
	if _, _, ok := New("https://api.anthropic.com").ExtractCompressible([]byte("not json"), providers.RequestMetadata{Endpoint: "/v1/messages"}); ok {
		t.Fatal("malformed JSON must opt out")
	}
}

// TestExtractStabilizable_TagsFrozenAndLiveBlocks pins the zone tagging the
// cross-turn prefix fix depends on: the clamped trailing turn is Live (it may be
// newly compressed), every earlier user block is frozen (substitute-only), and
// assistant blocks are never exposed at all because the proxy never compresses them.
func TestExtractStabilizable_TagsFrozenAndLiveBlocks(t *testing.T) {
	older := strings.Repeat("OLDER_TURN ", 70)
	prior := strings.Repeat("PRIOR_TURN ", 70)
	live := strings.Repeat("NEWEST_TURN ", 70)
	assistant := strings.Repeat("ASSISTANT_TURN ", 70)
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"` + older + `","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"` + assistant + `"}]},` +
		`{"role":"user","content":[{"type":"text","text":"` + prior + `","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"user","content":[{"type":"text","text":"` + live + `","cache_control":{"type":"ephemeral"}}]}` +
		`]}`)
	adapter := New("https://api.anthropic.com").(Adapter)

	blocks, reassemble, ok := adapter.ExtractStabilizable(body, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"})
	if !ok {
		t.Fatal("ExtractStabilizable ok=false")
	}
	want := []struct {
		text string
		live bool
		kind string
	}{{older, false, "history"}, {prior, false, "history"}, {live, true, "history"}}
	if len(blocks) != len(want) {
		t.Fatalf("blocks = %d, want %d", len(blocks), len(want))
	}
	for i, w := range want {
		if string(blocks[i].Content) != w.text || blocks[i].Live != w.live || blocks[i].Kind != w.kind {
			t.Fatalf("block %d = live:%v %q..., want live:%v %q...", i, blocks[i].Live, string(blocks[i].Content)[:12], w.live, w.text[:12])
		}
	}

	// The reassembler still byte-splices: substituting a frozen block and leaving the
	// rest nil must change only that block's bytes.
	out, err := reassemble([][]byte{[]byte("SUBSTITUTED"), nil, nil})
	if err != nil {
		t.Fatalf("reassemble: %v", err)
	}
	if !bytes.Contains(out, []byte(`"text":"SUBSTITUTED"`)) {
		t.Fatalf("frozen block was not substituted: %s", out)
	}
	if !bytes.Contains(out, []byte(prior)) || !bytes.Contains(out, []byte(live)) || !bytes.Contains(out, []byte(assistant)) {
		t.Fatalf("untouched blocks must stay byte-identical: %s", out)
	}
	if !json.Valid(out) {
		t.Fatalf("splice produced invalid JSON: %s", out)
	}
}

// TestExtractStabilizable_UnmarkedConversationFreezesOlderUserTurns covers the
// no-cache_control variant: only the latest user turn is Live, but earlier user
// turns are still exposed as frozen so a replacement emitted when they WERE latest
// can be substituted back in.
func TestExtractStabilizable_UnmarkedConversationFreezesOlderUserTurns(t *testing.T) {
	first := strings.Repeat("FIRST_TURN ", 70)
	second := strings.Repeat("SECOND_TURN ", 70)
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[` +
		`{"role":"user","content":"` + first + `"},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":"` + second + `"}` +
		`]}`)
	adapter := New("https://api.anthropic.com").(Adapter)

	blocks, _, ok := adapter.ExtractStabilizable(body, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"})
	if !ok {
		t.Fatal("ExtractStabilizable ok=false")
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	if blocks[0].Live || string(blocks[0].Content) != first {
		t.Fatalf("block 0 should be the frozen first turn, got live:%v", blocks[0].Live)
	}
	if !blocks[1].Live || string(blocks[1].Content) != second {
		t.Fatalf("block 1 should be the live latest turn, got live:%v", blocks[1].Live)
	}
	if blocks[0].Kind != "history" || blocks[1].Kind != "history" {
		t.Fatalf("semantic kinds = %q,%q, want history,history", blocks[0].Kind, blocks[1].Kind)
	}
}

func TestExtractStabilizable_TagsToolResultSemantics(t *testing.T) {
	toolResult := strings.Repeat(`{"row":1,"ok":true}`, 40)
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + strconv.Quote(toolResult) + `}]}` +
		`]}`)
	blocks, _, ok := New("https://api.anthropic.com").(Adapter).ExtractStabilizable(
		body,
		providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"},
	)
	if !ok || len(blocks) != 1 {
		t.Fatalf("tool-result extraction = ok:%v blocks:%d", ok, len(blocks))
	}
	if blocks[0].Kind != "tool_result" || !blocks[0].Live {
		t.Fatalf("tool-result block = kind:%q live:%v", blocks[0].Kind, blocks[0].Live)
	}
}

// TestExtractStabilizable_CountTokensOptsOut keeps the count_tokens carve-out: that
// endpoint is never reshaped, so it exposes no blocks at all.
func TestExtractStabilizable_CountTokensOptsOut(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"` + strings.Repeat("X ", 400) + `"}]}`)
	if _, _, ok := New("https://api.anthropic.com").(Adapter).ExtractStabilizable(body, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages/count_tokens"}); ok {
		t.Fatal("count_tokens must opt out")
	}
}

func TestFrozenPrefixBytesTracksExactFrozenWireAndIgnoresLiveTail(t *testing.T) {
	adapter := New("https://api.anthropic.com").(Adapter)
	body := func(system, frozen, live string) []byte {
		return []byte(`{"system":[{"type":"text","text":` + strconv.Quote(system) +
			`,"cache_control":{"type":"ephemeral"}}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"messages":[` +
			`{"role":"user","content":[{"type":"text","text":` + strconv.Quote(frozen) +
			`,"cache_control":{"type":"ephemeral"}}]},` +
			`{"role":"assistant","content":"ok"},` +
			`{"role":"user","content":` + strconv.Quote(live) + `}]}`)
	}
	meta := providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}
	first, ok := adapter.FrozenPrefixComponents(body("system", "frozen", "live one"), meta)
	if !ok {
		t.Fatal("frozen prefix unavailable")
	}
	second, ok := adapter.FrozenPrefixComponents(body("system", "frozen", "live two"), meta)
	if !ok || !equalFrozenComponents(first, second) {
		t.Fatal("live tail changed frozen prefix evidence")
	}
	drifted, ok := adapter.FrozenPrefixComponents(body("changed", "frozen", "live two"), meta)
	if !ok || equalFrozenComponents(first, drifted) {
		t.Fatal("system byte drift did not change frozen prefix evidence")
	}
	if _, ok := adapter.FrozenPrefixComponents(body("system", "frozen", "live"), providers.RequestMetadata{Endpoint: "/v1/messages/count_tokens"}); ok {
		t.Fatal("count_tokens must not claim provider prefix boundary evidence")
	}
}

func equalFrozenComponents(first, second [][]byte) bool {
	if len(first) != len(second) {
		return false
	}
	for i := range first {
		if !bytes.Equal(first[i], second[i]) {
			return false
		}
	}
	return true
}
