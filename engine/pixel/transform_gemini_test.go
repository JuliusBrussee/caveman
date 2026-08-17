// Built on the caveman pixel primitives ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"strings"
	"testing"
)

func TestTransformGeminiStructural(t *testing.T) {
	req := map[string]any{
		"model": "gemini-3.6-flash",
		"systemInstruction": map[string]any{
			"parts": []any{
				map[string]any{"text": strings.Repeat("Stable operating instruction. Keep exact policy boundaries.\n", 420)},
				map[string]any{"text": "<env>\nWorking directory: /repo\n</env>"},
			},
		},
		"tools": []any{
			map[string]any{
				"functionDeclarations": []any{
					map[string]any{
						"name":        "lookup",
						"description": "Return lookup results.",
						"parameters": map[string]any{
							"type":       "object",
							"properties": map[string]any{"q": map[string]any{"type": "string"}},
						},
					},
				},
			},
		},
		"contents": append(geminiHistoryFixtureContents(14),
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "RECENT_TAIL_0"}}},
			map[string]any{"role": "model", "parts": []any{map[string]any{"functionCall": map[string]any{"name": "lookup", "args": map[string]any{"q": "recent"}}}}},
			map[string]any{"role": "user", "parts": []any{
				map[string]any{"functionResponse": map[string]any{
					"name":     "lookup",
					"response": map[string]any{"result": strings.Repeat("recent function response payload with useful rows\n", 700)},
				}},
				map[string]any{"text": "RECENT_TAIL_2"},
			}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "RECENT_TAIL_3"}}},
		),
		"generationConfig": map[string]any{"temperature": 0},
		"stream":           true,
		"x_unknown":        map[string]any{"keep": []any{3, 2, 1}},
	}
	body := mustJSONGemini(t, req)
	out, info, err := TransformGemini(body, TransformOptions{Model: "gemini-3.6-flash"})
	if err != nil {
		t.Fatalf("TransformGemini error: %v", err)
	}
	if !info.Compressed || info.ImageCount < 3 {
		t.Fatalf("not compressed enough: %+v", info)
	}
	if info.TextTokensEstimate <= info.ImageTokensEstimate || info.ImageTokensEstimate <= 0 {
		t.Fatalf("bad token estimates: before=%d after=%d", info.TextTokensEstimate, info.ImageTokensEstimate)
	}

	var got geminiGenerateContent
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal transformed: %v", err)
	}
	if got.SystemInstruction == nil {
		t.Fatal("systemInstruction missing")
	}
	if hasGeminiInlineData(got.SystemInstruction.Parts) {
		t.Fatalf("systemInstruction must stay text-only: %+v", got.SystemInstruction.Parts)
	}
	if !hasGeminiTextContaining(got.SystemInstruction.Parts, "Rendered session configuration images") {
		t.Fatalf("systemInstruction pointer missing: %+v", got.SystemInstruction.Parts)
	}
	if !hasGeminiText(got.SystemInstruction.Parts, "<env>\nWorking directory: /repo\n</env>") {
		t.Fatal("dynamic system tag was not preserved as text")
	}
	if len(got.Contents) < 5 {
		t.Fatalf("history collapse did not keep synthetic plus tail: %d contents", len(got.Contents))
	}
	if got.Contents[0].Role != "user" || len(got.Contents[0].Parts) < 2 || got.Contents[0].Parts[0].InlineData == nil {
		t.Fatalf("rendered system images not prepended to first user content: %+v", got.Contents[0])
	}
	if !hasGeminiText(got.Contents[0].Parts, "[End of rendered context.]") {
		t.Fatalf("rendered context boundary missing from first user content: %+v", got.Contents[0].Parts)
	}
	if !hasGeminiText(got.Contents[0].Parts, geminiHistoryIntro) {
		t.Fatalf("synthetic history intro missing: %+v", got.Contents[0])
	}
	if !hasGeminiInlineData(got.Contents[0].Parts) {
		t.Fatal("synthetic history has no PNG inline_data parts")
	}
	if !hasGeminiInlineData(got.Contents[3].Parts) {
		t.Fatalf("recent functionResponse was not imaged: %+v", got.Contents[3].Parts)
	}
	assertGeminiFunctionResponsesFollowCalls(t, got)
	if !hasGeminiText(got.Contents[len(got.Contents)-1].Parts, "RECENT_TAIL_3") {
		t.Fatal("recent tail text was not preserved")
	}
	for _, part := range allGeminiParts(got) {
		if part.InlineData == nil {
			continue
		}
		if part.InlineData.MimeType != "image/png" {
			t.Fatalf("mime type = %q", part.InlineData.MimeType)
		}
		data, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
		if err != nil {
			t.Fatalf("bad inline_data base64: %v", err)
		}
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			t.Fatalf("inline_data is not decodable PNG: %v", err)
		}
	}

	var outMap map[string]json.RawMessage
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("unmarshal top-level: %v", err)
	}
	if string(outMap["stream"]) != "true" {
		t.Fatalf("stream field changed: %s", outMap["stream"])
	}
	var gotTools, wantTools any
	if err := json.Unmarshal(outMap["tools"], &gotTools); err != nil {
		t.Fatalf("tools unmarshal: %v", err)
	}
	if err := json.Unmarshal(mustRawMessage(t, req["tools"]), &wantTools); err != nil {
		t.Fatalf("want tools unmarshal: %v", err)
	}
	if !jsonDeepEqual(gotTools, wantTools) {
		t.Fatalf("tools changed: got=%s", outMap["tools"])
	}
	var unknown map[string]any
	if err := json.Unmarshal(outMap["x_unknown"], &unknown); err != nil {
		t.Fatalf("unknown unmarshal: %v", err)
	}
	if !jsonDeepEqual(unknown, map[string]any{"keep": []any{float64(3), float64(2), float64(1)}}) {
		t.Fatalf("unknown changed: %#v", unknown)
	}
}

func TestTransformGeminiPassThroughByteIdentity(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"small"}]}],"stream":true}`)
	out, info, err := TransformGemini(body, TransformOptions{Model: "gemini-3.6-flash"})
	if err != nil {
		t.Fatalf("TransformGemini error: %v", err)
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("pass-through bytes changed:\ngot  %s\nwant %s", out, body)
	}
	if info.ImageCount != 0 || info.Compressed {
		t.Fatalf("unexpected compression info: %+v", info)
	}

	opts := DefaultTransformOptions("gemini-3.6-flash")
	opts.Compress = false
	out, info, err = TransformGemini([]byte(`{"systemInstruction":{"parts":[{"text":"`+strings.Repeat("long policy ", 3000)+`"}]},"contents":[{"role":"user","parts":[{"text":"small"}]}]}`), opts)
	if err != nil {
		t.Fatalf("compress=false returned error: %v", err)
	}
	if info.Compressed || info.ImageCount != 0 || info.Reason != "compress=false" {
		t.Fatalf("compress=false ignored: %+v", info)
	}
	if out == nil {
		t.Fatal("compress=false returned nil body")
	}
}

func TestTransformGeminiUnknownModelFailsClosedBeforeTokenGate(t *testing.T) {
	body := []byte(`{"systemInstruction":{"parts":[{"text":"` + strings.Repeat("long policy ", 5000) + `"}]},"contents":[{"role":"user","parts":[{"text":"small"}]}]}`)
	out, info, err := TransformGemini(body, TransformOptions{Model: "future-gemini"})
	if err != nil {
		t.Fatalf("TransformGemini error: %v", err)
	}
	if !bytes.Equal(out, body) || info.Compressed || info.ImageCount != 0 {
		t.Fatalf("unsupported model changed request: compressed=%v images=%d", info.Compressed, info.ImageCount)
	}
	if info.Reason != "unsupported_image_token_profile" {
		t.Fatalf("reason = %q, want unsupported_image_token_profile", info.Reason)
	}
}

func TestTransformGeminiNonHighGlobalMediaResolutionFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
	}{
		{"snake-low", "generation_config", "MEDIA_RESOLUTION_LOW"},
		{"camel-medium", "generationConfig", "MEDIA_RESOLUTION_MEDIUM"},
		{"unknown", "generation_config", "MEDIA_RESOLUTION_FUTURE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field := "media_resolution"
			if tc.key == "generationConfig" {
				field = "mediaResolution"
			}
			body := mustJSONGemini(t, map[string]any{
				"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": strings.Repeat("long policy ", 5000)}}},
				"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "small"}}}},
				tc.key:              map[string]any{field: tc.value},
			})
			out, info, err := TransformGemini(body, TransformOptions{Model: "gemini-3.6-flash"})
			if err != nil {
				t.Fatalf("TransformGemini error: %v", err)
			}
			if !bytes.Equal(out, body) || info.Compressed || info.ImageCount != 0 {
				t.Fatalf("unsupported media resolution changed request: %+v", info)
			}
			if info.Reason != "unsupported_media_resolution" {
				t.Fatalf("reason = %q, want unsupported_media_resolution", info.Reason)
			}
		})
	}
	body := mustJSONGemini(t, map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": strings.Repeat("long policy ", 5000)}}},
		"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "small"}}}},
		"generationConfig":  map[string]any{"mediaResolution": "MEDIA_RESOLUTION_HIGH"},
		"generation_config": map[string]any{"media_resolution": "MEDIA_RESOLUTION_HIGH"},
	})
	out, info, err := TransformGemini(body, TransformOptions{Model: "gemini-3.6-flash"})
	if err != nil {
		t.Fatalf("TransformGemini conflict error: %v", err)
	}
	if !bytes.Equal(out, body) || info.Reason != "unsupported_media_resolution" {
		t.Fatalf("conflicting media resolution shapes must pass through: %+v", info)
	}
}

func TestTransformGeminiExplicitHighMediaResolutionUsesHighBudget(t *testing.T) {
	body := mustJSONGemini(t, map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": strings.Repeat("long policy ", 5000)}}},
		"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "small"}}}},
		"generation_config": map[string]any{"media_resolution": "MEDIA_RESOLUTION_HIGH"},
	})
	out, info, err := TransformGemini(body, TransformOptions{Model: "gemini-3.6-flash"})
	if err != nil {
		t.Fatalf("TransformGemini error: %v", err)
	}
	if bytes.Equal(out, body) || !info.Compressed || info.ImageCount == 0 {
		t.Fatalf("explicit high fixture should transform: %+v", info)
	}
	if info.ImageTokensEstimate != info.ImageCount*1232 {
		t.Fatalf("high token estimate = %d for %d images, want 1232/image", info.ImageTokensEstimate, info.ImageCount)
	}
}

func TestTransformGeminiMalformedBody(t *testing.T) {
	out, info, err := TransformGemini([]byte(`{"contents":[`), TransformOptions{Model: "gemini-3.6-flash"})
	if err == nil {
		t.Fatal("expected malformed-body error")
	}
	if out != nil {
		t.Fatalf("malformed body returned output: %s", out)
	}
	if !strings.HasPrefix(info.Reason, "parse_error:") {
		t.Fatalf("reason = %q", info.Reason)
	}
}

func TestTransformGeminiUnknownTopLevelRawPreserved(t *testing.T) {
	body := []byte(`{"systemInstruction":{"parts":[{"text":"` + strings.Repeat("policy line with many chars ", 1500) + `"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}],"x_raw":{"b":[2,1],"a":"x"}}`)
	out, info, err := TransformGemini(body, TransformOptions{Model: "gemini-3.6-flash"})
	if err != nil {
		t.Fatalf("TransformGemini error: %v", err)
	}
	if info.ImageCount == 0 {
		t.Fatalf("expected system image: %+v", info)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if string(got["x_raw"]) != `{"b":[2,1],"a":"x"}` {
		t.Fatalf("x_raw changed: %s", got["x_raw"])
	}
}

func TestTransformGeminiUsesModelSpecificMediaResolutionGate(t *testing.T) {
	text := strings.Repeat("x", 8000)
	eval := evalGeminiProfitability("gemini-3.6-flash", "default", text, DenseContentCols, 10, 1, CharsPerToken, 0, 0, true, DenseContentCharsPerImage, true)
	if eval == nil || !eval.profitable || eval.imageTokens != 1232 { // 1120 high/default * 1.10 margin
		t.Fatalf("unexpected Gemini 3 media-resolution gate: %+v", eval)
	}
	body := mustJSONGemini(t, map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}}},
	})
	out, info, err := TransformGemini(body, TransformOptions{Model: "gemini-3.6-flash"})
	if err != nil {
		t.Fatalf("TransformGemini error: %v", err)
	}
	if bytes.Equal(out, body) {
		t.Fatalf("Gemini 3 profitable fixture should transform")
	}
	if info.ImageCount != 1 || info.ImageTokensEstimate != 1232 {
		t.Fatalf("unexpected transformed image accounting: %+v", info)
	}
}

func TestTransformGeminiKeepsFunctionResponsePairing(t *testing.T) {
	payload := strings.Repeat("lookup row with useful payload fields and values\n", 1200)
	response := map[string]any{
		"functionResponse": map[string]any{
			"name":     "lookup",
			"response": map[string]any{"result": payload},
		},
	}
	body := mustJSONGemini(t, map[string]any{
		"contents": []any{
			map[string]any{"role": "model", "parts": []any{
				map[string]any{"functionCall": map[string]any{"name": "lookup", "args": map[string]any{"q": "recent"}}},
			}},
			map[string]any{"role": "user", "parts": []any{response}},
		},
	})
	out, info, err := TransformGemini(body, TransformOptions{Model: "gemini-3.6-flash", CharsPerToken: 1})
	if err != nil {
		t.Fatalf("TransformGemini error: %v", err)
	}
	if info.ToolResultImgs == 0 || info.ImageCount == 0 {
		t.Fatalf("functionResponse was not imaged: %+v", info)
	}
	var got geminiGenerateContent
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal transformed: %v", err)
	}
	assertGeminiFunctionResponsesFollowCalls(t, got)
	parts := got.Contents[1].Parts
	if len(parts) < 2 || parts[0].FunctionResponse == nil || parts[0].FunctionResponse.Name != "lookup" {
		t.Fatalf("functionResponse stub not kept in place: %+v", parts)
	}
	var stub map[string]map[string]any
	if err := json.Unmarshal(parts[0].FunctionResponse.Response, &stub); err != nil {
		t.Fatalf("functionResponse stub invalid JSON: %v", err)
	}
	if stub["caveman_pixel"]["payload_rendered_to_following_images"] != true {
		t.Fatalf("stub does not disclose rendered payload: %s", parts[0].FunctionResponse.Response)
	}
	if stub["caveman_pixel"]["original_char_count"].(float64) <= float64(len(payload)) {
		t.Fatalf("stub original char count too small: %s", parts[0].FunctionResponse.Response)
	}
	if !hasGeminiInlineData(parts[1:]) {
		t.Fatalf("rendered images missing after functionResponse stub: %+v", parts)
	}
}

func geminiHistoryFixtureContents(n int) []any {
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "model"
		}
		out = append(out, map[string]any{
			"role": role,
			"parts": []any{
				map[string]any{"text": strings.Repeat("old turn payload with file paths /tmp/repo/file.go and IDs ABC-1234\n", 35)},
			},
		})
	}
	return out
}

func mustJSONGemini(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustRawMessage(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hasGeminiInlineData(parts []geminiPart) bool {
	for _, part := range parts {
		if part.InlineData != nil {
			return true
		}
	}
	return false
}

func hasGeminiText(parts []geminiPart, want string) bool {
	for _, part := range parts {
		if part.Text != nil && *part.Text == want {
			return true
		}
	}
	return false
}

func hasGeminiTextContaining(parts []geminiPart, want string) bool {
	for _, part := range parts {
		if part.Text != nil && strings.Contains(*part.Text, want) {
			return true
		}
	}
	return false
}

func assertGeminiFunctionResponsesFollowCalls(t *testing.T, req geminiGenerateContent) {
	t.Helper()
	open := make(map[string]int)
	for _, content := range req.Contents {
		for _, part := range content.Parts {
			if part.FunctionCall != nil {
				open[part.FunctionCall.Name]++
			}
			if part.FunctionResponse != nil {
				name := part.FunctionResponse.Name
				if open[name] == 0 {
					continue
				}
				open[name]--
				if open[name] == 0 {
					delete(open, name)
				}
			}
		}
	}
	if len(open) != 0 {
		t.Fatalf("functionCall without following functionResponse: %+v", open)
	}
}

func allGeminiParts(req geminiGenerateContent) []geminiPart {
	var out []geminiPart
	if req.SystemInstruction != nil {
		out = append(out, req.SystemInstruction.Parts...)
	}
	for _, c := range req.Contents {
		out = append(out, c.Parts...)
	}
	return out
}

func jsonDeepEqual(a, b any) bool {
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}
