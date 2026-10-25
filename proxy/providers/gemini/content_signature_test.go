package gemini

import (
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestCompressionPreservesSignedAndThoughtParts(t *testing.T) {
	adapter := New("https://generativelanguage.googleapis.com").(Adapter)
	meta := providers.RequestMetadata{Endpoint: "/v1beta/models/gemini:generateContent"}
	text := quote(t, strings.Repeat("signed provider content ", 50))
	for _, field := range []string{`"thoughtSignature":"opaque"`, `"thought_signature":"opaque"`, `"thought":true`} {
		for _, part := range []string{
			`{"text":` + text + `,` + field + `}`,
			`{"functionResponse":{"name":"lookup","response":{"output":` + text + `}},` + field + `}`,
		} {
			body := []byte(`{"contents":[{"role":"user","parts":[` + part + `]}]}`)
			if _, _, ok := adapter.ExtractCompressible(body, meta); ok {
				t.Fatalf("signed/thought part exposed for compression: %s", field)
			}
			if _, _, ok := adapter.ExtractStabilizable(body, meta); ok {
				t.Fatalf("signed/thought part exposed for cached replacement: %s", field)
			}
		}
	}
}
