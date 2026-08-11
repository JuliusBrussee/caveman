package vertex

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// S19 regression: the Vertex adapter must pass the caller's Application Default
// Credentials (a fresh OAuth2 bearer token per request) straight through to the
// upstream Authorization header and persist NOTHING — the gateway holds no GCP
// secret. Statelessness is the "no persistence" guarantee at the adapter layer:
// the adapter struct carries no credential field, and two requests with different
// tokens each forward their own token with no bleed from a cached first token.
func TestVertexADCPassThroughNoPersistence(t *testing.T) {
	a := newAdapter(t)

	// (1) ADC pass-through: the caller-supplied token becomes the upstream bearer.
	const tokenA = "ya29.adc-token-AAA"
	reqA, _ := http.NewRequest(http.MethodPost, predictPath("google", geminiModel, "generateContent"), nil)
	outA, err := a.SanitizeAndMapHeaders(context.Background(), reqA, providers.Credential{Key: tokenA}, nil)
	if err != nil {
		t.Fatalf("sanitize A: %v", err)
	}
	if outA.Get("Authorization") != "Bearer "+tokenA {
		t.Errorf("request A Authorization = %q, want Bearer %s (ADC pass-through)", outA.Get("Authorization"), tokenA)
	}

	// (2) No persistence: a second request with a DIFFERENT token forwards ITS token,
	// proving the adapter retained nothing from request A.
	const tokenB = "ya29.adc-token-BBB"
	reqB, _ := http.NewRequest(http.MethodPost, predictPath("anthropic", "claude-3-5-sonnet", "rawPredict"), nil)
	outB, err := a.SanitizeAndMapHeaders(context.Background(), reqB, providers.Credential{Key: tokenB}, nil)
	if err != nil {
		t.Fatalf("sanitize B: %v", err)
	}
	if outB.Get("Authorization") != "Bearer "+tokenB {
		t.Errorf("request B Authorization = %q, want Bearer %s (no token persisted from A)", outB.Get("Authorization"), tokenB)
	}
	// Token A must not bleed into request B's headers.
	for name, vals := range outB {
		for _, v := range vals {
			if strings.Contains(v, tokenA) {
				t.Errorf("token A leaked into request B header %s = %q (adapter persisted a credential)", name, v)
			}
		}
	}

	// (3) The adapter forwards the credential ONLY as the upstream Authorization — it
	// is never echoed into any cave/inbound header (no persistence surface).
	for name, vals := range outA {
		if strings.EqualFold(name, "authorization") {
			continue
		}
		for _, v := range vals {
			if strings.Contains(v, tokenA) {
				t.Errorf("ADC token appears in non-auth header %s = %q", name, v)
			}
		}
	}
}
