package azureopenai

import (
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestExtractCompressibleReusesOpenAIChatGrammar(t *testing.T) {
	live := strings.Repeat("AZURE_LIVE ", 60)
	body := []byte(`{"messages":[{"role":"user","content":"` + live + `"}]}`)
	adapter := New("https://example.openai.azure.com").(Adapter)
	segments, reassemble, ok := adapter.ExtractCompressible(body, providers.RequestMetadata{Endpoint: "/azure/openai/deployments/prod/chat/completions"})
	if !ok || len(segments) != 1 || string(segments[0]) != live {
		t.Fatalf("segments=%q ok=%v", segments, ok)
	}
	out, err := reassemble([][]byte{[]byte("SHORT")})
	if err != nil || !strings.Contains(string(out), `"content":"SHORT"`) {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

// prefixStabilizer mirrors the gateway's optional adapter capability. Azure must
// satisfy it the same way it inherits compression, or Azure conversations would
// compress a turn and then flip the prefix back to the client's originals on the
// next one.
type prefixStabilizer interface {
	ExtractStabilizable(body []byte, meta providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool)
}

func TestExtractStabilizableInheritsOpenAIZones(t *testing.T) {
	older := strings.Repeat("AZURE_OLDER ", 60)
	live := strings.Repeat("AZURE_LIVE ", 60)
	body := []byte(`{"messages":[` +
		`{"role":"user","content":"` + older + `"},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":"` + live + `"}]}`)
	adapter, ok := New("https://example.openai.azure.com").(prefixStabilizer)
	if !ok {
		t.Fatal("azure adapter must expose ExtractStabilizable")
	}
	blocks, _, ok := adapter.ExtractStabilizable(body, providers.RequestMetadata{Endpoint: "/azure/openai/deployments/prod/chat/completions"})
	if !ok || len(blocks) != 2 {
		t.Fatalf("blocks=%d ok=%v", len(blocks), ok)
	}
	if blocks[0].Live || string(blocks[0].Content) != older {
		t.Fatalf("block 0 should be the frozen earlier turn, got live:%v", blocks[0].Live)
	}
	if !blocks[1].Live || string(blocks[1].Content) != live {
		t.Fatalf("block 1 should be the live latest turn, got live:%v", blocks[1].Live)
	}
}
