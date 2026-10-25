package providers_test

import (
	"bytes"
	"encoding/json"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/gemini"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/shared/toolpolicy"
	"strings"
	"testing"
)

func TestToolPreservationBeforeEveryAdapterRewrite(t *testing.T) {
	payload := strings.Repeat(`{"symbol":"Parser.parse","path":"src/parser.ts","signature":"parse(x: string): Node","calls":["Node.new"],"error":"not found","count":42,"source":"function parse(x: string): Node { return Node.new(x); }"} `, 20)
	q, _ := json.Marshal(payload)
	p := string(q)
	cases := []struct {
		name, endpoint, body string
		adapter              providers.Adapter
	}{
		{"anthropic", "/v1/messages", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"mcp__tokensave__tokensave_context"},{"type":"tool_use","id":"b","name":"Read"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":` + p + `},{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":` + p + `}]}]}]}`, anthropic.New("")},
		{"chat", "/v1/chat/completions", `{"messages":[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"mcp__tokensave__tokensave_context"}},{"id":"b","type":"function","function":{"name":"Read"}}]},{"role":"tool","tool_call_id":"b","content":` + p + `},{"role":"tool","tool_call_id":"a","content":` + p + `}]}`, openai.New("")},
		{"responses", "/v1/responses", `{"input":[{"type":"function_call","call_id":"a","name":"mcp__tokensave__tokensave_context"},{"type":"function_call","call_id":"b","name":"Read"},{"type":"function_call_output","call_id":"b","output":` + p + `},{"type":"function_call_output","call_id":"a","output":` + p + `}]}`, openai.New("")},
		{"gemini", "/v1beta/models/gemini:generateContent", `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"Read","response":{"output":` + p + `}}},{"functionResponse":{"name":"mcp__tokensave__tokensave_context","response":{"output":` + p + `}}}]}]}`, gemini.New("")},
	}
	originals := append([]struct {
		name, endpoint, body string
		adapter              providers.Adapter
	}(nil), cases...)
	for _, tc := range originals {
		bare := tc
		bare.name += "-bare"
		bare.body = strings.ReplaceAll(tc.body, "mcp__tokensave__tokensave_context", "tokensave_context")
		cases = append(cases, bare)
		if tc.name == "responses" {
			custom := tc
			custom.name = "responses-custom"
			custom.body = strings.ReplaceAll(tc.body, "function_call", "custom_tool_call")
			cases = append(cases, custom)
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := providers.RequestMetadata{Endpoint: tc.endpoint, PreserveTools: toolpolicy.NewMatcher(toolpolicy.Policy{"mcp__tokensave__*", "tokensave_*"})}
			body := []byte(tc.body)
			// Mutate every offered block, including frozen substitutions. The protected
			// serialized payload must still be present exactly, with no recovery dependency.
			stable := tc.adapter.(interface {
				ExtractStabilizable([]byte, providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool)
			})
			blocks, splice, ok := stable.ExtractStabilizable(body, meta)
			if !ok || len(blocks) != 1 {
				t.Fatalf("expected unrelated result only, got %d, %v", len(blocks), ok)
			}
			out, err := splice([][]byte{[]byte("CHANGED")})
			if err != nil || !bytes.Contains(out, q) || !bytes.Contains(out, []byte("CHANGED")) {
				t.Fatalf("protected payload altered: %s; %v", out, err)
			}
			live := tc.adapter
			segs, reassemble, liveOK := live.ExtractCompressible(body, meta)
			if liveOK {
				replacements := make([][]byte, len(segs))
				for i := range replacements {
					replacements[i] = []byte("CHANGED")
				}
				out, err = reassemble(replacements)
				if err != nil || !bytes.Contains(out, q) {
					t.Fatalf("live rewrite altered protected content: %s", out)
				}
			}
			// Removing policy restores ordinary compression eligibility.
			meta.PreserveTools = toolpolicy.Matcher{}
			all, _, ok := stable.ExtractStabilizable(body, meta)
			if !ok || len(all) != 2 {
				t.Fatalf("normal behavior: got %d", len(all))
			}
		})
	}
}
func TestUnresolvedResultsAndDuplicateIDs(t *testing.T) {
	calls := map[string]bool{"protected": true, "ordinary": false, "": false}
	for _, id := range []string{"protected", "missing", ""} {
		if !providers.ProtectedResult(calls, id, true) {
			t.Fatal(id)
		}
	}
	if providers.ProtectedResult(calls, "ordinary", true) || providers.ProtectedResult(calls, "missing", false) {
		t.Fatal("unrelated/default behavior changed")
	}
}

func TestPreservationAuditReportsAmbiguityWithoutPayloads(t *testing.T) {
	meta := providers.RequestMetadata{Endpoint: "/v1/responses", PreserveTools: toolpolicy.NewMatcher(toolpolicy.Policy{"tokensave_*"})}
	body := []byte(`{"input":[{"type":"function_call","call_id":"a","name":"Read"},{"type":"function_call","call_id":"a","name":"tokensave_context"},{"type":"function_call_output","call_id":"a","output":"secret payload"},{"type":"function_call_output","call_id":"orphan","output":"secret payload"}]}`)
	report := providers.AuditPreservation(body, meta)
	if !report.Supported || report.AmbiguousIDs != 1 || report.ProtectedResults != 2 || report.UnresolvedResults != 1 || len(report.Tools) != 1 || report.Tools[0] != "tokensave_context" {
		t.Fatalf("%+v", report)
	}
	meta.Endpoint = "/unsupported"
	if providers.AuditPreservation(body, meta).Supported {
		t.Fatal("unsupported protocol claimed")
	}
}
