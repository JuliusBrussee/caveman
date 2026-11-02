package pixel

import (
	"bytes"
	"strings"
	"testing"
)

func TestTransformGeminiPreservesOpaquePartMetadata(t *testing.T) {
	for _, field := range []string{"thoughtSignature", "thought_signature", "thought", "provider_metadata"} {
		t.Run(field, func(t *testing.T) {
			part := map[string]any{"text": strings.Repeat("Exact signed response payload with useful rows\n", 700), field: "opaque"}
			if field == "thought" {
				part[field] = true
			}
			body := mustJSONGemini(t, map[string]any{"contents": []any{
				map[string]any{"role": "model", "parts": []any{part}},
				map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Continue"}}},
			}})
			out, info, err := TransformGemini(body, TransformOptions{Model: "gemini-3.6-flash"})
			if err != nil || info.Compressed || !bytes.Equal(out, body) {
				t.Fatalf("opaque provider part changed: err=%v compressed=%v", err, info.Compressed)
			}
		})
	}
}
