// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const openAIBigSystem = "System instruction with lots of detail. "
const openAIBigToolDesc = "Tool description with lots of context. "

func TestGptProfilesAndVisionCosts(t *testing.T) {
	t.Setenv("CAVE_PIXEL_GPT_PROFILES", "")
	if OpenAIVisionTokens("gpt-5", 768, 1932) != 1190 {
		t.Fatalf("gpt-5 768x1932 cost mismatch")
	}
	if OpenAIVisionTokens("gpt-4o", 768, 1932) != 1445 {
		t.Fatalf("gpt-4o 768x1932 cost mismatch")
	}
	if OpenAIVisionTokens("gpt-5-mini", 768, 1932) != 2372 {
		t.Fatalf("gpt-5-mini 768x1932 cost mismatch")
	}
	if ResolveGptProfile("o4-mini").Vision.Regime != "patch" {
		t.Fatalf("o4-mini should use patch regime")
	}
	if OpenAIVisionTokens("o4-mini", 768, 1932) != 2372 {
		t.Fatalf("o4-mini 768x1932 cost mismatch")
	}
	if OpenAIVisionTokens("gpt-5", 2048, 2048) != 630 {
		t.Fatalf("gpt-5 2048x2048 cost mismatch")
	}
	if ResolveGptProfile("gpt-5.6").Vision.Regime != "patch" {
		t.Fatalf("gpt-5.6 should use patch regime")
	}
	if ResolveGptProfile("o1").Vision.Base != 75 {
		t.Fatalf("o1 should use 75/150 tile profile")
	}

	t.Setenv("CAVE_PIXEL_GPT_PROFILES", `{"gpt-5.6":{"stripCols":176},"gpt-5.6-special":{"vision":{"regime":"tile","base":1,"perTile":2},"maxHeightPx":2400}}`)
	if got := ResolveGptProfile("gpt-5.6-alpha").StripCols; got != 176 {
		t.Fatalf("partial env override stripCols=%d", got)
	}
	special := ResolveGptProfile("gpt-5.6-special-v1")
	if special.Vision.Regime != "tile" || special.Vision.Base != 1 || special.MaxHeightPx != 2400 {
		t.Fatalf("longest-prefix env override not applied: %+v", special)
	}
}

func TestGptProfileEnvRejectsInvalidCostsAndSubPixelGeometry(t *testing.T) {
	t.Setenv("CAVE_PIXEL_GPT_PROFILES", `{
		"gpt-negative-tile":{"vision":{"regime":"tile","base":-1,"perTile":2}},
		"gpt-negative-patch":{"vision":{"regime":"patch","multiplier":-1,"patchCap":1536}},
		"gpt-subpixel":{"stripCols":0.5,"maxHeightPx":0.5},
		"gpt-overflow":{"vision":{"regime":"patch","multiplier":1e308,"patchCap":1536},"stripCols":1e308,"maxHeightPx":1e308}
	}`)

	want := resolveBuiltinGptProfile("gpt-4o")
	if got := ResolveGptProfile("gpt-negative-tile"); got.Vision != want.Vision {
		t.Fatalf("negative tile price accepted: %+v", got.Vision)
	}
	if got := ResolveGptProfile("gpt-negative-patch"); got.Vision != want.Vision {
		t.Fatalf("negative patch price accepted: %+v", got.Vision)
	}
	if got := ResolveGptProfile("gpt-subpixel"); got.StripCols != want.StripCols || got.MaxHeightPx != want.MaxHeightPx {
		t.Fatalf("sub-pixel geometry accepted: %+v", got)
	}
	if got := ResolveGptProfile("gpt-overflow"); got.Vision != want.Vision || got.StripCols != want.StripCols || got.MaxHeightPx != want.MaxHeightPx {
		t.Fatalf("overflowing profile accepted: %+v", got)
	}
}

func TestTransformOpenAIChatCompressesSystemAndToolDocs(t *testing.T) {
	body := mustMarshalTest(t, map[string]any{
		"model":    "gpt-5.6",
		"metadata": map[string]any{"preserved": true},
		"messages": []any{
			map[string]any{"role": "system", "content": strings.Repeat(openAIBigSystem, 500)},
			map[string]any{"role": "user", "content": "hello"},
		},
		"tools": []any{taskLikeChatTool(strings.Repeat(openAIBigToolDesc, 200))},
	})
	opts := openAITestOptions("gpt-5.6")
	out, info, err := TransformOpenAI(body, opts)
	if err != nil {
		t.Fatalf("TransformOpenAI: %v", err)
	}
	if !info.Compressed || info.ImageCount == 0 {
		t.Fatalf("expected compression, info=%+v", info)
	}
	if info.TextTokensEstimate <= info.ImageTokensEstimate {
		t.Fatalf("expected inferred text tokens > image tokens, before=%d after=%d", info.TextTokensEstimate, info.ImageTokensEstimate)
	}
	if strings.Contains(string(out), "cache"+"_"+"control") {
		t.Fatalf("OpenAI path emitted Anthropic marker")
	}

	var got map[string]any
	unmarshalTest(t, out, &got)
	if got["metadata"].(map[string]any)["preserved"] != true {
		t.Fatalf("unknown top-level metadata not preserved")
	}
	messages := got["messages"].([]any)
	system := messages[0].(map[string]any)
	if !strings.Contains(system["content"].(string), "rendered into image") {
		t.Fatalf("system pointer missing: %v", system["content"])
	}
	slab := messages[1].(map[string]any)
	parts := slab["content"].([]any)
	if parts[0].(map[string]any)["type"] != "image_url" {
		t.Fatalf("first slab part should be image_url")
	}
	if detail := parts[0].(map[string]any)["image_url"].(map[string]any)["detail"]; detail != "high" {
		t.Fatalf("chat image detail = %v, want provider-valid high", detail)
	}
	if countDataImages(got) != info.ImageCount {
		t.Fatalf("image count mismatch")
	}
	tools := got["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["description"].(string) == "" {
		t.Fatalf("native tool description should remain")
	}
	params := fn["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)
	if _, ok := props["description"]; !ok {
		t.Fatalf("property named description was removed")
	}
	if _, ok := props["description"].(map[string]any)["description"]; ok {
		t.Fatalf("schema prose should be stripped from native JSON")
	}
	if !reflect.DeepEqual(params["required"], []any{"description", "prompt"}) {
		t.Fatalf("required list drifted: %#v", params["required"])
	}
}

func TestTransformOpenAIResponsesCompressesInstructionsAndTools(t *testing.T) {
	body := mustMarshalTest(t, map[string]any{
		"model":        "gpt-5.6",
		"metadata":     map[string]any{"preserved": true},
		"instructions": strings.Repeat("These are detailed instructions. ", 600),
		"input": []any{
			map[string]any{"role": "user", "content": "Please do the thing."},
		},
		"tools": []any{taskLikeResponsesTool(strings.Repeat("Flat tool description with lots of context. ", 200))},
	})
	opts := openAITestOptions("gpt-5.6")
	out, info, err := TransformOpenAI(body, opts)
	if err != nil {
		t.Fatalf("TransformOpenAI: %v", err)
	}
	if !info.Compressed || info.ImageCount == 0 {
		t.Fatalf("expected compression, info=%+v", info)
	}
	var got map[string]any
	unmarshalTest(t, out, &got)
	if !strings.Contains(got["instructions"].(string), "rendered into image") {
		t.Fatalf("instructions pointer missing")
	}
	input := got["input"].([]any)
	slab := input[0].(map[string]any)
	parts := slab["content"].([]any)
	if parts[0].(map[string]any)["type"] != "input_image" {
		t.Fatalf("first slab part should be input_image")
	}
	if detail := parts[0].(map[string]any)["detail"]; detail != "high" {
		t.Fatalf("responses image detail = %v, want provider-valid high", detail)
	}
	tool := got["tools"].([]any)[0].(map[string]any)
	params := tool["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)
	if _, ok := props["description"]; !ok {
		t.Fatalf("property named description was removed")
	}
	if _, ok := props["description"].(map[string]any)["description"]; ok {
		t.Fatalf("schema prose should be stripped from native JSON")
	}
}

func TestTransformOpenAIResponsesBareStringInput(t *testing.T) {
	body := mustMarshalTest(t, map[string]any{
		"model":        "gpt-5.6",
		"instructions": strings.Repeat("These are detailed instructions. ", 600),
		"input":        "Do the thing please.",
	})
	opts := openAITestOptions("gpt-5.6")
	out, info, err := TransformOpenAI(body, opts)
	if err != nil {
		t.Fatalf("TransformOpenAI: %v", err)
	}
	if !info.Compressed {
		t.Fatalf("expected compression, info=%+v", info)
	}
	var got map[string]any
	unmarshalTest(t, out, &got)
	input := got["input"].([]any)
	parts := input[0].(map[string]any)["content"].([]any)
	if parts[0].(map[string]any)["type"] != "input_image" {
		t.Fatalf("first bare input part should be image")
	}
	if !containsTextPart(parts, "Do the thing please.") {
		t.Fatalf("original bare string not preserved as text part")
	}
}

func TestTransformOpenAIPassThroughAndErrors(t *testing.T) {
	small := mustMarshalTest(t, map[string]any{
		"model": "gpt-5.6",
		"messages": []any{
			map[string]any{"role": "system", "content": "short"},
			map[string]any{"role": "user", "content": "hi"},
		},
	})
	out, info, err := TransformOpenAI(small, DefaultTransformOptions("gpt-5.6"))
	if err != nil {
		t.Fatalf("small input should not error: %v", err)
	}
	if !bytes.Equal(out, small) || info.Compressed {
		t.Fatalf("small input should pass through")
	}
	badOut, _, badErr := TransformOpenAI([]byte(`{"messages":[`), DefaultTransformOptions("gpt-5.6"))
	if badErr == nil || badOut != nil {
		t.Fatalf("malformed JSON should return nil body and error")
	}
}

func TestTransformOpenAIGoldenFixtures(t *testing.T) {
	// These fixtures (gpt-5.6) pin conservative rendered output byte-for-byte.
	// Density defaults to balanced for gpt-5.6, so pin conservative here.
	t.Setenv("CAVE_PIXEL_DENSITY", "off")
	type manifest struct {
		Entries []struct {
			ID         string `json:"id"`
			Shape      string `json:"shape"`
			InputFile  string `json:"inputFile"`
			OutputFile string `json:"outputFile"`
			ImageCount int    `json:"imageCount"`
			Images     []struct {
				File string `json:"file"`
			} `json:"images"`
		} `json:"entries"`
	}
	var mf manifest
	unmarshalFileTest(t, "testdata/transform_openai/manifest.json", &mf)
	for _, entry := range mf.Entries {
		t.Run(entry.ID, func(t *testing.T) {
			input := readFileTest(t, filepath.Join("testdata/transform_openai", entry.InputFile))
			opts := openAITestOptions("gpt-5.6")
			out, info, err := TransformOpenAI(input, opts)
			if err != nil {
				t.Fatalf("TransformOpenAI: %v", err)
			}
			if info.ImageCount != entry.ImageCount {
				t.Fatalf("image count=%d want %d", info.ImageCount, entry.ImageCount)
			}
			var got, want any
			unmarshalTest(t, out, &got)
			unmarshalFileTest(t, filepath.Join("testdata/transform_openai", entry.OutputFile), &want)
			gotImages := collectDataImageStrings(got)
			wantImages := collectDataImageStrings(want)
			if len(gotImages) != len(wantImages) {
				t.Fatalf("data image count got %d want %d", len(gotImages), len(wantImages))
			}
			if !reflect.DeepEqual(normalizeDataImages(got), normalizeDataImages(want)) {
				t.Fatalf("normalized output structure/text drifted")
			}
			for i, uri := range gotImages {
				gotImg := decodeDataImageTest(t, uri)
				wantPNG := readFileTest(t, filepath.Join("testdata/transform_openai", entry.Images[i].File))
				wantImg := decodePNGTest(t, wantPNG)
				assertImagePixelsEqual(t, gotImg, wantImg)
			}
		})
	}
}

func openAITestOptions(model string) TransformOptions {
	opts := DefaultTransformOptions(model)
	opts.MinCompressChars = 1
	opts.CharsPerToken = 1
	opts.CollapseHistory = false
	return opts
}

func taskLikeParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description": map[string]any{"type": "string", "description": "A short description of the task"},
			"prompt":      map[string]any{"type": "string", "description": "The task for the agent to perform"},
			"title":       map[string]any{"type": "string", "description": "Property name collides with title keyword"},
		},
		"required":             []any{"description", "prompt"},
		"additionalProperties": false,
	}
}

func taskLikeChatTool(desc string) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "task",
			"description": desc,
			"parameters":  taskLikeParams(),
		},
	}
}

func taskLikeResponsesTool(desc string) map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "task",
		"description": desc,
		"parameters":  taskLikeParams(),
	}
}

func mustMarshalTest(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func unmarshalTest(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func unmarshalFileTest(t *testing.T, path string, v any) {
	t.Helper()
	unmarshalTest(t, readFileTest(t, path), v)
}

func readFileTest(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func countDataImages(v any) int {
	return len(collectDataImageStrings(v))
}

func collectDataImageStrings(v any) []string {
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			if strings.HasPrefix(t, "data:image/png;base64,") {
				out = append(out, t)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		case map[string]any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(v)
	return out
}

func normalizeDataImages(v any) any {
	switch t := v.(type) {
	case string:
		if strings.HasPrefix(t, "data:image/png;base64,") {
			return "<image>"
		}
		return t
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = normalizeDataImages(child)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[k] = normalizeDataImages(child)
		}
		return out
	default:
		return v
	}
}

func decodeDataImageTest(t *testing.T, uri string) image.Image {
	t.Helper()
	raw := strings.TrimPrefix(uri, "data:image/png;base64,")
	pngBytes, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	return decodePNGTest(t, pngBytes)
}

func decodePNGTest(t *testing.T, pngBytes []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func assertImagePixelsEqual(t *testing.T, got, want image.Image) {
	t.Helper()
	if got.Bounds() != want.Bounds() {
		t.Fatalf("bounds got %v want %v", got.Bounds(), want.Bounds())
	}
	for y := got.Bounds().Min.Y; y < got.Bounds().Max.Y; y++ {
		for x := got.Bounds().Min.X; x < got.Bounds().Max.X; x++ {
			gr, gg, gb, ga := got.At(x, y).RGBA()
			wr, wg, wb, wa := want.At(x, y).RGBA()
			if gr != wr || gg != wg || gb != wb || ga != wa {
				t.Fatalf("pixel mismatch at %d,%d", x, y)
			}
		}
	}
}

func containsTextPart(parts []any, text string) bool {
	for _, part := range parts {
		m, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if strings.Contains(stringOr(m["text"], ""), text) {
			return true
		}
	}
	return false
}
