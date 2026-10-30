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

// TestEveryRecoveryToolSpellingIsExempt pins the exemption to every spelling in
// the providers.IsRecoveryToolName allowlist, not just the plain MCP one.
//
// #1432: `caveman wrap claude` registers the recovery MCP server inside its
// ephemeral `caveman-wrap` plugin, and Claude Code namespaces a plugin's MCP
// server as `plugin:<plugin>:<server>` before building the tool name, so the
// tool the model actually calls is
// `mcp__plugin_caveman-wrap_caveman__caveman_retrieve`. That spelling was
// missing from the allowlist, so the retrieve result was NOT exempt and the
// prefix cache handed the agent back the very elision it was escaping — the
// "Same truncation" failure, on the default `caveman claude` door.
func TestEveryRecoveryToolSpellingIsExempt(t *testing.T) {
	for _, toolName := range []string{
		"caveman_retrieve",
		"mcp__caveman__caveman_retrieve",
		"caveman_caveman_retrieve",
		"mcp__plugin_caveman-wrap_caveman__caveman_retrieve",
	} {
		t.Run(toolName, func(t *testing.T) {
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

			serveBody(t, srv, "/v1/messages", `{"model":"claude-sonnet-4-6","messages":[`+anthropicReadTurn(true)+`]}`, headers)
			if !strings.Contains(string(rt.bodies[0]), "ELIDED") {
				t.Fatalf("precondition: first read should have been compressed: %s", rt.bodies[0])
			}

			retrieve := strings.Replace(anthropicRetrieveTurn(), "mcp__caveman__caveman_retrieve", toolName, 1)
			serveBody(t, srv, "/v1/messages", `{"model":"claude-sonnet-4-6","messages":[`+anthropicReadTurn(true)+`,`+retrieve+`]}`, headers)

			upstream := string(rt.bodies[1])
			retrieveIdx := strings.Index(upstream, "toolu_ret")
			if retrieveIdx < 0 {
				t.Fatalf("retrieve turn missing upstream: %s", upstream)
			}
			if tail := upstream[retrieveIdx:]; !strings.Contains(tail, recoveredBlock) {
				t.Fatalf("recovered bytes were recompressed for %s; the agent has no other way back.\nretrieve tail: %s", toolName, tail)
			}
		})
	}
}
