package gemini

import (
	"bytes"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

var recoveredPayload = strings.Repeat("bibliography entry the agent must enumerate exhaustively ", 40)

// rewritable returns the blocks the proxy may rewrite. An ok=false result means
// the adapter found nothing rewritable at all, which is the strongest form of
// "not a candidate" — so it is reported as an empty set, not a failure.
func rewritable(t *testing.T, body []byte) []providers.RewritableBlock {
	t.Helper()
	blocks, _, ok := Adapter{}.ExtractStabilizable(body, providers.RequestMetadata{
		Provider: "gemini",
		Endpoint: "/v1beta/models/gemini-2.5-pro:generateContent",
	})
	if !ok {
		return nil
	}
	return blocks
}

func contains(blocks []providers.RewritableBlock, want string) bool {
	for _, b := range blocks {
		if bytes.Contains(b.Content, []byte(want)) {
			return true
		}
	}
	return false
}

// A functionResponse from caveman_retrieve carries the exact bytes an earlier
// elision removed. Rewriting it hands the agent back its own elision.
func TestRecoveryFunctionResponseIsNotRewritable(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[` +
		`{"functionResponse":{"name":"mcp__caveman__caveman_retrieve","response":{"output":"` + recoveredPayload + `"}}}]}]}`)
	if contains(rewritable(t, body), recoveredPayload) {
		t.Fatal("recovered functionResponse must not be rewritable")
	}
}

func TestOrdinaryFunctionResponseStaysRewritable(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[` +
		`{"functionResponse":{"name":"read_file","response":{"output":"` + recoveredPayload + `"}}}]}]}`)
	if !contains(rewritable(t, body), recoveredPayload) {
		t.Fatal("an ordinary functionResponse must remain rewritable")
	}
}
