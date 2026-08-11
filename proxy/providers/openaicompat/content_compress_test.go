package openaicompat

import (
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestNamedAdapterReusesOpenAIResponsesGrammar(t *testing.T) {
	live := strings.Repeat("COMPAT_LIVE ", 60)
	body := []byte(`{"model":"x","input":"` + live + `"}`)
	adapter, err := NewNamed("groq", "https://api.groq.com/openai")
	if err != nil {
		t.Fatal(err)
	}
	segments, reassemble, ok := adapter.ExtractCompressible(body, providers.RequestMetadata{Endpoint: "/compat/groq/v1/responses"})
	if !ok || len(segments) != 1 || string(segments[0]) != live {
		t.Fatalf("segments=%q ok=%v", segments, ok)
	}
	out, err := reassemble([][]byte{[]byte("SHORT")})
	if err != nil || !strings.Contains(string(out), `"input":"SHORT"`) {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

// prefixStabilizer mirrors the gateway's optional adapter capability. BOTH compat
// adapter shapes must satisfy it the same way they inherit compression, or a compat
// conversation would compress a turn and then flip the prefix back to the client's
// originals on the next one.
type prefixStabilizer interface {
	ExtractStabilizable(body []byte, meta providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool)
}

func TestExtractStabilizableInheritsOpenAIZones(t *testing.T) {
	older := strings.Repeat("COMPAT_OLDER ", 60)
	live := strings.Repeat("COMPAT_LIVE ", 60)
	body := []byte(`{"model":"x","messages":[` +
		`{"role":"user","content":"` + older + `"},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":"` + live + `"}]}`)
	named, err := NewNamed("groq", "https://api.groq.com/openai")
	if err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string]providers.Adapter{
		"mount": New("https://api.groq.com/openai"),
		"named": named,
	} {
		t.Run(name, func(t *testing.T) {
			adapter, ok := candidate.(prefixStabilizer)
			if !ok {
				t.Fatal("compat adapter must expose ExtractStabilizable")
			}
			blocks, _, ok := adapter.ExtractStabilizable(body, providers.RequestMetadata{Endpoint: "/compat/groq/v1/chat/completions"})
			if !ok || len(blocks) != 2 {
				t.Fatalf("blocks=%d ok=%v", len(blocks), ok)
			}
			if blocks[0].Live || string(blocks[0].Content) != older {
				t.Fatalf("block 0 should be the frozen earlier turn, got live:%v", blocks[0].Live)
			}
			if !blocks[1].Live || string(blocks[1].Content) != live {
				t.Fatalf("block 1 should be the live latest turn, got live:%v", blocks[1].Live)
			}
		})
	}
}
