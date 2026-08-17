package openai

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
func rewritable(t *testing.T, body []byte, endpoint string) []providers.RewritableBlock {
	t.Helper()
	blocks, _, ok := ExtractStabilizable(body, providers.RequestMetadata{Provider: "openai", Endpoint: endpoint})
	if !ok {
		return nil
	}
	return blocks
}

func blockContains(blocks []providers.RewritableBlock, want string) bool {
	for _, b := range blocks {
		if bytes.Contains(b.Content, []byte(want)) {
			return true
		}
	}
	return false
}

// A tool message answering caveman_retrieve carries the exact bytes an earlier
// elision removed. Exposing it as rewritable lets the prefix cache substitute
// that same elision straight back in, leaving the agent no path to its own data.
func TestChatRecoveryToolResultIsNotRewritable(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[` +
		`{"role":"user","content":"find the fake citations"},` +
		`{"role":"assistant","tool_calls":[{"id":"call_ret","type":"function","function":{"name":"mcp__caveman__caveman_retrieve","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"call_ret","content":"` + recoveredPayload + `"}]}`)

	blocks := rewritable(t, body, "/v1/chat/completions")
	if blockContains(blocks, recoveredPayload) {
		t.Fatalf("recovered tool message must not be rewritable; got %d blocks", len(blocks))
	}
}

// An ordinary tool message stays rewritable — the exemption must be keyed on the
// recovery tool, not on tool messages generally.
func TestChatOrdinaryToolResultStaysRewritable(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[` +
		`{"role":"user","content":"find the fake citations"},` +
		`{"role":"assistant","tool_calls":[{"id":"call_read","type":"function","function":{"name":"Read","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"call_read","content":"` + recoveredPayload + `"}]}`)

	blocks := rewritable(t, body, "/v1/chat/completions")
	if !blockContains(blocks, recoveredPayload) {
		t.Fatal("an ordinary tool result must remain rewritable")
	}
}

func TestResponsesRecoveryOutputIsNotRewritable(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"find the fake citations"}]},` +
		`{"type":"function_call","call_id":"call_ret","name":"caveman_retrieve","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_ret","output":"` + recoveredPayload + `"}]}`)

	blocks := rewritable(t, body, "/v1/responses")
	if blockContains(blocks, recoveredPayload) {
		t.Fatalf("recovered function_call_output must not be rewritable; got %d blocks", len(blocks))
	}
}

func TestResponsesOrdinaryOutputStaysRewritable(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"find the fake citations"}]},` +
		`{"type":"function_call","call_id":"call_read","name":"Read","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_read","output":"` + recoveredPayload + `"}]}`)

	blocks := rewritable(t, body, "/v1/responses")
	if !blockContains(blocks, recoveredPayload) {
		t.Fatal("an ordinary function_call_output must remain rewritable")
	}
}
