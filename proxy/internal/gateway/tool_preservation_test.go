package gateway

import (
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/shared/toolpolicy"
	"strings"
	"testing"
)

func TestProtectedSemanticResultReachesUpstreamUnchanged(t *testing.T) {
	for _, mode := range []string{"compress", "pixel"} {
		t.Run(mode, func(t *testing.T) {
			rt := &captureTransport{responses: []string{subMessageRespBody}}
			comp := &liveZoneCompressor{outputs: [][]byte{[]byte("ELIDED")}}
			cfg := Config{RecoveryViaMCP: true, PrefixCache: newTestPrefixCache(), PreserveTools: toolpolicy.Policy{"mcp__tokensave__*"}}
			srv := newSocketFreeCompressServerWithConfig(anthropic.New("https://upstream.test"), comp, rt, passthroughTestCreds{}, cfg)
			// This test reaches the normal compress gateway. Pixel's independent scanner
			// is checked directly below; model allowlisting must not bypass protection.
			body := `{"model":"claude-sonnet-4-6","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"mcp__tokensave__tokensave_context"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"` + recoveredBlock + `"}]}]}`
			if mode == "compress" {
				serveBody(t, srv, "/v1/messages", body, map[string]string{"anthropic-version": "2023-06-01"})
				if len(rt.bodies) != 1 || string(rt.bodies[0]) != body {
					t.Fatalf("protected request changed: %s", rt.bodies)
				}
			} else {
				meta := providers.RequestMetadata{Provider: "anthropic", Model: "claude-fable-5", PreserveTools: toolpolicy.NewMatcher(cfg.PreserveTools)}
				transform := providers.TransformResult{Body: []byte(body)}
				if got := srv.pixelRequest([]byte(body), meta, &transform, "test"); got != nil || string(transform.Body) != body {
					t.Fatal("pixel must pass through with preservation")
				}
			}
		})
	}
}

func TestCatalogStripDisabledWithPreservation(t *testing.T) {
	srv := New(Config{ToolSchemaStrip: toolSchemaStripMode, PreserveTools: toolpolicy.Policy{"tokensave_*"}})
	if srv.toolSchemaStripApplies(anthropic.New(""), []byte(`{"tools":[]}`)) {
		t.Fatal("catalog strip must not bypass preservation")
	}
}

func TestProtectedResultIgnoresExistingCompressedReplacement(t *testing.T) {
	rt := &captureTransport{responses: []string{subMessageRespBody, subMessageRespBody}}
	comp := &liveZoneCompressor{outputs: [][]byte{[]byte("ELIDED"), []byte("ELIDED")}}
	srv := newSocketFreeCompressServerWithConfig(anthropic.New("https://upstream.test"), comp, rt, passthroughTestCreds{}, Config{
		SubscriptionCompress: "live_zone", RecoveryViaMCP: true, PrefixCache: newTestPrefixCache(), PreserveTools: toolpolicy.Policy{"mcp__tokensave__*"},
	})
	headers := map[string]string{"user-agent": "claude-cli/2.1", "authorization": "Bearer sk-ant-oat-test", "anthropic-beta": "oauth-2025-04-20", "anthropic-version": "2023-06-01"}
	first := `{"model":"claude-sonnet-4-6","messages":[` + anthropicReadTurn(true) + `]}`
	serveBody(t, srv, "/v1/messages", first, headers)
	if !strings.Contains(string(rt.bodies[0]), "ELIDED") {
		t.Fatal("ordinary read was not compressed")
	}
	semantic := strings.ReplaceAll(anthropicRetrieveTurn(), "mcp__caveman__caveman_retrieve", "mcp__tokensave__tokensave_context")
	second := `{"model":"claude-sonnet-4-6","messages":[` + anthropicReadTurn(true) + `,` + semantic + `]}`
	serveBody(t, srv, "/v1/messages", second, headers)
	upstream := string(rt.bodies[1])
	idx := strings.Index(upstream, "toolu_ret")
	if idx < 0 || !strings.Contains(upstream[idx:], recoveredBlock) {
		t.Fatalf("cached elision replaced protected result: %s", upstream)
	}
}
