package pixel

import (
	"strings"
	"testing"
)

func TestTransformOpenAIChatCompressesDenseSystemSlab(t *testing.T) {
	t.Setenv("CAVE_PIXEL_GPT_PROFILES", "")
	body := []byte(`{"model":"gpt-5.6","messages":[{"role":"system","content":"` + strings.Repeat(`OPENAI_SYSTEM_CONTEXT detailed instruction.\n`, 3000) + `"},{"role":"user","content":"hello"}]}`)

	out, info, err := TransformOpenAI(body, DefaultTransformOptions("gpt-5.6"))
	if err != nil {
		t.Fatalf("TransformOpenAI: %v", err)
	}
	if len(out) == 0 || !info.Compressed || info.ImageCount == 0 {
		t.Fatalf("expected dense slab compression, info=%+v outLen=%d", info, len(out))
	}
	if info.ImageTokensEstimate <= 0 || info.TextTokensEstimate <= 0 {
		t.Fatalf("missing token estimates: before=%d after=%d", info.TextTokensEstimate, info.ImageTokensEstimate)
	}
	if info.ImageTokensEstimate >= info.TextTokensEstimate {
		t.Fatalf("expected image estimate below text estimate, before=%d after=%d", info.TextTokensEstimate, info.ImageTokensEstimate)
	}
}
