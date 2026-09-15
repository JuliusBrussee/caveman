// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestTransformAnthropicCacheControlNeverAdds(t *testing.T) {
	body := mustJSONBytes(t, map[string]any{
		"model":  "claude-fable-5",
		"system": strings.Repeat("static slab ", 12000),
		"messages": []any{
			map[string]any{"role": "user", "content": "go"},
		},
	})
	out, info, err := TransformAnthropic(body, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || !info.Compressed {
		t.Fatalf("expected transform, info=%+v", info)
	}
	if countCacheMarkersJSON(t, out) != 0 {
		t.Fatalf("transform added cache_control marker")
	}
}

func TestTransformAnthropicRelocatesExistingCacheControlToHistory(t *testing.T) {
	body := mustJSONBytes(t, map[string]any{
		"model":    "claude-fable-5",
		"system":   []any{map[string]any{"type": "text", "text": strings.Repeat("static slab ", 12000), "cache_control": map[string]any{"type": "ephemeral"}}},
		"messages": messagesToAny(anthropicConvo(56, 3500)),
	})
	out, info, err := TransformAnthropic(body, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || info.CollapsedTurns != 49 || info.CollapsedImages < 2 {
		t.Fatalf("expected carry-over collapse, info=%+v", info)
	}
	var got any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	positions := sortedCacheControlPositions(got)
	if len(positions) != 1 {
		t.Fatalf("marker count=%d positions=%v", len(positions), positions)
	}
	if !strings.Contains(positions[0], ".messages[1].content") {
		t.Fatalf("marker not relocated to history image: %v", positions)
	}
}

func TestTransformAnthropicKeepSharpAndRecoverable(t *testing.T) {
	big := strings.Repeat("tool-result-exact-id-abc123\n", 2500)
	// Non-hi-res model → std geometry, where this 2500-narrow-line tool result is
	// profitable to image. (On a hi-res model the same content spreads across
	// full-width hi-res pages and the gate honestly declines — a distinct case.)
	body := mustJSONBytes(t, map[string]any{
		"model":  "claude-3-5-sonnet-20241022",
		"system": strings.Repeat("static slab ", 12000),
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "keep_me", "content": big},
				map[string]any{"type": "tool_result", "tool_use_id": "image_me", "content": big},
			},
		}},
	})
	opts := TransformOptions{CharsPerToken: 2, EmitRecoverable: true}
	opts.KeepSharp = func(block KeepSharpBlock) bool { return block.ToolUseID == "keep_me" }
	out, info, err := TransformAnthropic(body, opts)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Fatalf("expected transformed body")
	}
	if info.KeptSharpBlocks != 1 {
		t.Fatalf("kept sharp blocks=%d, want 1", info.KeptSharpBlocks)
	}
	if len(info.Recoverable) != 1 || info.Recoverable[0].ToolUseID != "image_me" {
		t.Fatalf("unexpected recoverable entries: %+v", info.Recoverable)
	}
	wantID := "rec_" + sha8ForTest("tool_result\x00image_me\x00"+big)
	if info.Recoverable[0].ID != wantID {
		t.Fatalf("recoverable id=%s want %s", info.Recoverable[0].ID, wantID)
	}
	blocks := userBlocksFromBody(t, out)
	kept := findToolResult(t, blocks, "keep_me")
	if kept["content"] != big {
		t.Fatalf("kept tool_result changed")
	}
	imaged := findToolResult(t, blocks, "image_me")
	content, ok := imaged["content"].([]any)
	if !ok || len(collectImageData(content)) == 0 {
		t.Fatalf("image_me was not imaged: %+v", imaged["content"])
	}
}

func TestTransformAnthropicStaticTagCanary(t *testing.T) {
	resetStaticTagObservationsForTest()
	body1 := mustJSONBytes(t, map[string]any{
		"model":  "claude-fable-5",
		"system": "stable prefix\n<newPerTurnTag>alpha</newPerTurnTag>\nstable suffix",
		"messages": []any{
			map[string]any{"role": "user", "content": "same first user"},
		},
	})
	_, info1, err := TransformAnthropic(body1, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(info1.UnknownStaticTags, []string{"newPerTurnTag"}) {
		t.Fatalf("unknown static tags = %#v", info1.UnknownStaticTags)
	}
	if len(info1.ChurningStaticTags) != 0 {
		t.Fatalf("first sighting should not churn: %#v", info1.ChurningStaticTags)
	}

	body2 := mustJSONBytes(t, map[string]any{
		"model":  "claude-fable-5",
		"system": "stable prefix\n<newPerTurnTag>beta</newPerTurnTag>\nstable suffix",
		"messages": []any{
			map[string]any{"role": "user", "content": "same first user"},
		},
	})
	_, info2, err := TransformAnthropic(body2, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(info2.ChurningStaticTags, []string{"newPerTurnTag"}) {
		t.Fatalf("churning static tags = %#v", info2.ChurningStaticTags)
	}
}

func TestObserveStaticTagChurnConcurrent(t *testing.T) {
	resetStaticTagObservationsForTest()
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = observeStaticTagChurn("session-"+strconv.Itoa(g%4), map[string]string{
					"tag": strconv.Itoa(g) + "-" + strconv.Itoa(i),
				})
			}
		}()
	}
	wg.Wait()
	staticTagObservations.mu.Lock()
	size := staticTagObservations.order.Len()
	staticTagObservations.mu.Unlock()
	if size > tagObservationsMax {
		t.Fatalf("tag observation LRU size=%d max=%d", size, tagObservationsMax)
	}
}

func TestTransformAnthropicMalformedAndPassThroughShapes(t *testing.T) {
	out, info, err := TransformAnthropic([]byte(`{"model":`), TransformOptions{})
	if err == nil || out != nil || !strings.HasPrefix(info.Reason, "parse_error:") {
		t.Fatalf("bad parse contract: out=%v info=%+v err=%v", out, info, err)
	}

	body := mustJSONBytes(t, map[string]any{
		"model":            "claude-fable-5",
		"unknown_top":      map[string]any{"nested": true},
		"anthropic_beta":   []any{"x"},
		"messages":         []any{map[string]any{"role": "user", "content": "short content string"}},
		"top_raw_survives": "yes",
	})
	out, info, err = TransformAnthropic(body, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out != nil || info.ImageCount != 0 {
		t.Fatalf("short content should pass through: out=%v info=%+v", out != nil, info)
	}
}

func TestTransformAnthropicHistoryRunsOnEarlyExit(t *testing.T) {
	body := mustJSONBytes(t, map[string]any{
		"model":    "claude-fable-5",
		"system":   "short",
		"messages": messagesToAny(anthropicConvo(15, 3500)),
	})
	out, info, err := TransformAnthropic(body, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || !info.Compressed || info.HistoryReason != "collapsed" || info.CollapsedTurns != 10 {
		t.Fatalf("history did not collapse on early exit: out=%v info=%+v", out != nil, info)
	}
	if info.TextTokensEstimate <= info.ImageTokensEstimate || info.ImageTokensEstimate == 0 {
		t.Fatalf("bad token estimates: before=%d after=%d", info.TextTokensEstimate, info.ImageTokensEstimate)
	}
}

func TestTransformAnthropicGoldenFixtures(t *testing.T) {
	// These fixtures pin the transform's std-canvas conservative output byte-for-
	// byte (decoded image pixels + structure). They read as a non-hi-res model
	// (claude-3-5-sonnet), which ResolveDensity fails closed to conservative + std
	// even at the default (balanced) env — so this is the std path end-to-end.
	// Hi-res balanced geometry is covered by TestTransformAnthropicHiResBalancedGeometry.
	root := filepath.Join("testdata", "transform_anthropic")
	var manifest struct {
		Cases []struct {
			Name     string `json:"name"`
			Input    string `json:"input"`
			Expected string `json:"expected"`
		} `json:"cases"`
	}
	readJSONFile(t, filepath.Join(root, "manifest.json"), &manifest)
	if len(manifest.Cases) != 3 {
		t.Fatalf("fixture count=%d, want 3", len(manifest.Cases))
	}
	for _, tc := range manifest.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join(root, tc.Input))
			if err != nil {
				t.Fatal(err)
			}
			var expected any
			readJSONFile(t, filepath.Join(root, tc.Expected), &expected)
			out, info, err := TransformAnthropic(input, TransformOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if out == nil || !info.Compressed {
				t.Fatalf("no transform output, info=%+v", info)
			}
			var got any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			assertCachePositionsEqual(t, expected, got)
			assertMessagesStructureEqual(t, expected, got)
			assertDecodedImagesEqual(t, expected, got)
		})
	}
}

func assertMessagesStructureEqual(t *testing.T, expected any, got any) {
	t.Helper()
	expReq := expected.(map[string]any)
	gotReq := got.(map[string]any)
	expMessages := expReq["messages"].([]any)
	gotMessages := gotReq["messages"].([]any)
	if len(gotMessages) != len(expMessages) {
		t.Fatalf("message count got %d want %d", len(gotMessages), len(expMessages))
	}
	for i := range expMessages {
		em := expMessages[i].(map[string]any)
		gm := gotMessages[i].(map[string]any)
		if gm["role"] != em["role"] {
			t.Fatalf("message[%d] role got %v want %v", i, gm["role"], em["role"])
		}
		assertContentStructureEqual(t, "messages["+itoa(i)+"].content", em["content"], gm["content"])
	}
}

func assertContentStructureEqual(t *testing.T, path string, expected any, got any) {
	t.Helper()
	es, eok := expected.(string)
	gs, gok := got.(string)
	if eok || gok {
		if !eok || !gok || es != gs {
			t.Fatalf("%s string got %T/%q want %T/%q", path, got, gs, expected, es)
		}
		return
	}
	eb := expected.([]any)
	gb := got.([]any)
	if len(gb) != len(eb) {
		t.Fatalf("%s block count got %d want %d", path, len(gb), len(eb))
	}
	for i := range eb {
		em := eb[i].(map[string]any)
		gm := gb[i].(map[string]any)
		etype := em["type"]
		if gm["type"] != etype {
			t.Fatalf("%s[%d] type got %v want %v", path, i, gm["type"], etype)
		}
		if etype == "text" && gm["text"] != em["text"] {
			t.Fatalf("%s[%d] text mismatch", path, i)
		}
		if etype == "tool_result" {
			assertContentStructureEqual(t, path+"["+itoa(i)+"].content", em["content"], gm["content"])
		}
	}
}

func assertCachePositionsEqual(t *testing.T, expected any, got any) {
	t.Helper()
	want := sortedCacheControlPositions(expected)
	have := sortedCacheControlPositions(got)
	if !reflect.DeepEqual(have, want) {
		t.Fatalf("cache_control positions got %v want %v", have, want)
	}
}

func assertDecodedImagesEqual(t *testing.T, expected any, got any) {
	t.Helper()
	want := collectImageDataFromAny(expected)
	have := collectImageDataFromAny(got)
	if len(have) != len(want) {
		t.Fatalf("image count got %d want %d", len(have), len(want))
	}
	for i := range want {
		assertOneImagePixelsEqual(t, i, have[i], want[i])
	}
}

func assertOneImagePixelsEqual(t *testing.T, idx int, gotB64 string, wantB64 string) {
	t.Helper()
	gotImg := decodePNGFromB64(t, gotB64)
	wantImg := decodePNGFromB64(t, wantB64)
	if !gotImg.Bounds().Eq(wantImg.Bounds()) {
		t.Fatalf("image[%d] bounds got %v want %v", idx, gotImg.Bounds(), wantImg.Bounds())
	}
	b := gotImg.Bounds()
	diffs := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			gr, gg, gb, ga := gotImg.At(x, y).RGBA()
			wr, wg, wb, wa := wantImg.At(x, y).RGBA()
			if gr != wr || gg != wg || gb != wb || ga != wa {
				if diffs < 10 {
					t.Logf("image[%d] diff at (%d,%d): got=(%d,%d,%d,%d) want=(%d,%d,%d,%d)", idx, x, y, gr, gg, gb, ga, wr, wg, wb, wa)
				}
				diffs++
			}
		}
	}
	if diffs > 0 {
		t.Fatalf("image[%d] has %d pixel diffs", idx, diffs)
	}
}

func decodePNGFromB64(t *testing.T, s string) image.Image {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func userBlocksFromBody(t *testing.T, body []byte) []any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	for _, msgAny := range req["messages"].([]any) {
		msg := msgAny.(map[string]any)
		if msg["role"] == "user" {
			return msg["content"].([]any)
		}
	}
	t.Fatal("no user message")
	return nil
}

func findToolResult(t *testing.T, blocks []any, id string) map[string]any {
	t.Helper()
	for _, blockAny := range blocks {
		block := blockAny.(map[string]any)
		if block["type"] == "tool_result" && block["tool_use_id"] == id {
			return block
		}
	}
	t.Fatalf("missing tool_result %s", id)
	return nil
}

func collectImageDataFromMessages(messages []Message) []string {
	var root any = messagesToAny(messages)
	return collectImageDataFromAny(root)
}

func collectImageDataFromAny(v any) []string {
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case []any:
			for _, item := range t {
				walk(item)
			}
		case []Message:
			walk(messagesToAny(t))
		case map[string]any:
			if t["type"] == "image" {
				if source, ok := t["source"].(map[string]any); ok {
					out = append(out, toString(source["data"]))
				}
			}
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(v)
	return out
}

func collectImageData(blocks []any) []string {
	var out []string
	for _, blockAny := range blocks {
		block, _ := blockAny.(map[string]any)
		if block["type"] == "image" {
			source, _ := block["source"].(map[string]any)
			out = append(out, toString(source["data"]))
		}
	}
	return out
}

func messagesToAny(messages []Message) []any {
	out := make([]any, 0, len(messages))
	for _, msg := range messages {
		out = append(out, map[string]any{"role": msg.Role, "content": msg.Content})
	}
	return out
}

func countCacheMarkersJSON(t *testing.T, body []byte) int {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return len(sortedCacheControlPositions(v))
}

func mustJSONBytes(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readJSONFile(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

func sha8ForTest(text string) string {
	sum := sha256SumForTest([]byte(text))
	return base64HexForTest(sum[:4])
}

func sha256SumForTest(b []byte) [32]byte {
	return sha256.Sum256(b)
}

func base64HexForTest(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}

func resetStaticTagObservationsForTest() {
	staticTagObservations.mu.Lock()
	defer staticTagObservations.mu.Unlock()
	staticTagObservations.order.Init()
	staticTagObservations.items = make(map[string]*list.Element)
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
