package gateway

import (
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// recoveredBlock is the payload an agent reads, has elided, and then asks back
// through its own caveman_retrieve MCP tool.
var recoveredBlock = strings.Repeat("bibliography entry that the agent must enumerate exhaustively ", 40)

func anthropicReadTurn(marked bool) string {
	cc := ""
	if marked {
		cc = `,"cache_control":{"type":"ephemeral"}`
	}
	return `{"role":"assistant","content":[{"type":"tool_use","id":"toolu_read","name":"Read","input":{"file_path":"./test.bib"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_read","content":[{"type":"text","text":"` + recoveredBlock + `"` + cc + `}]}]}`
}

func anthropicRetrieveTurn() string {
	return `{"role":"assistant","content":[{"type":"tool_use","id":"toolu_ret","name":"mcp__caveman__caveman_retrieve","input":{"recovery_handle":"ccr_x"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_ret","content":[{"type":"text","text":"` + recoveredBlock + `"}]}]}`
}

// TestRecoveryResultIsNeverRecompressed is the citation-check regression.
//
// Measured on 2026-08-06: the agent read a file, saw it elided, called its own
// mcp__caveman__caveman_retrieve, and reported "Same truncation" — because the
// recovered bytes came back through the proxy as an ordinary live tool_result,
// hit the prefix cache under the SAME content key that produced the elision, and
// were substituted with the identical compressed replacement. Recovery was
// structurally impossible, so the agent re-read the file seven times.
//
// A retrieve result must reach the provider byte-for-byte.
func TestRecoveryResultIsNeverRecompressed(t *testing.T) {
	rt := &captureTransport{responses: []string{subMessageRespBody, subMessageRespBody}}
	comp := &liveZoneCompressor{outputs: [][]byte{[]byte("ELIDED"), []byte("ELIDED")}}
	srv := newSocketFreeCompressServerWithConfig(
		anthropic.New("https://upstream.test"),
		comp,
		rt,
		passthroughTestCreds{},
		Config{SubscriptionCompress: "live_zone", RecoveryViaMCP: true, PrefixCache: newTestPrefixCache()},
	)
	headers := map[string]string{
		"user-agent":        "claude-cli/2.1",
		"authorization":     "Bearer sk-ant-oat-test",
		"anthropic-beta":    "oauth-2025-04-20",
		"anthropic-version": "2023-06-01",
	}

	turn1 := `{"model":"claude-sonnet-4-6","messages":[` + anthropicReadTurn(true) + `]}`
	serveBody(t, srv, "/v1/messages", turn1, headers)
	if !strings.Contains(string(rt.bodies[0]), "ELIDED") {
		t.Fatalf("precondition: first read should have been compressed: %s", rt.bodies[0])
	}

	turn2 := `{"model":"claude-sonnet-4-6","messages":[` + anthropicReadTurn(true) + `,` + anthropicRetrieveTurn() + `]}`
	serveBody(t, srv, "/v1/messages", turn2, headers)

	upstream := string(rt.bodies[1])
	retrieveIdx := strings.Index(upstream, "toolu_ret")
	if retrieveIdx < 0 {
		t.Fatalf("retrieve turn missing upstream: %s", upstream)
	}
	tail := upstream[retrieveIdx:]
	if !strings.Contains(tail, recoveredBlock) {
		t.Fatalf("recovered bytes must reach the provider intact; the agent has no other way back.\nretrieve tail: %s", tail)
	}
}
