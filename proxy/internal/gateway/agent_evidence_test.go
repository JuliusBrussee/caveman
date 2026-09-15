package gateway

import (
	"net/http"
	"strings"
	"testing"
)

func TestRequestEvidenceFromHeadersValidatesContentBlindJoin(t *testing.T) {
	build := strings.Repeat("a", 64)
	plan := strings.Repeat("b", 64)
	prefix := strings.Repeat("c", 64)
	headers := http.Header{
		"X-Cave-Session":             {"support:session-1"},
		"X-Cave-Agent-Build":         {build},
		"X-Cave-Efficiency-Plan":     {plan},
		"X-Cave-Context-Bill":        {"instruction=12,user_intent=4"},
		"X-Cave-Transform-Trace":     {"caveman.engine.text.v1|history|S4|applied|10|4|exact_ccr|0|1"},
		"X-Cave-Transform-Location":  {"gateway"},
		"X-Cave-Cache-Epoch":         {"support:session-1:epoch-1"},
		"X-Cave-Cache-Prefix-Sha256": {prefix},
	}

	got := requestEvidenceFromHeaders(headers)
	if got.SessionID != "support:session-1" || got.AgentBuildSHA256 != build ||
		got.EfficiencyPlanSHA256 != plan || got.CachePrefixSHA256 != prefix ||
		got.TransformLocation != "gateway" || got.ContextBill == "" || got.TransformTrace == "" {
		t.Fatalf("unexpected evidence: %+v", got)
	}

	headers.Del("X-Cave-Efficiency-Plan")
	headers.Set("X-Cave-Session", "bad session")
	headers.Set("X-Cave-Context-Bill", "instruction=1\nforged=1")
	got = requestEvidenceFromHeaders(headers)
	if got.AgentBuildSHA256 != "" || got.EfficiencyPlanSHA256 != "" ||
		got.SessionID != "" || got.ContextBill != "" {
		t.Fatalf("malformed or partial identity survived: %+v", got)
	}
}
