package cachebench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/cacheengine"
)

func TestDefaultRealAgentSuiteClears97PercentGate(t *testing.T) {
	report, err := RunSimulated(context.Background(), cacheengine.New(cacheengine.Config{}), DefaultProviders(), DefaultScenario(), DefaultTarget())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !report.Overall.GatePassed || report.Status != "pass" {
		t.Fatalf("overall = %#v", report.Overall)
	}
	if report.Overall.RequestHitRate < 0.97 || report.Overall.TokenHitRate < 0.97 {
		t.Fatalf("rates = request %.6f token %.6f", report.Overall.RequestHitRate, report.Overall.TokenHitRate)
	}
	if report.Overall.OpportunityRequestCaptureRate != 1 || report.Overall.OpportunityTokenCaptureRate != 1 {
		t.Fatalf("opportunity capture = request %.6f token %.6f", report.Overall.OpportunityRequestCaptureRate, report.Overall.OpportunityTokenCaptureRate)
	}
	if len(report.Providers) != 4 {
		t.Fatalf("providers = %d", len(report.Providers))
	}
	for _, provider := range report.Providers {
		if !provider.GatePassed || provider.ColdWrites != 1 || provider.InvalidSamples != 0 || provider.SafetyFailures != 0 {
			t.Fatalf("provider %s = %#v", provider.Provider, provider)
		}
		if provider.Provider == "gemini" {
			if provider.AttributedTokenHitRate != 0 {
				t.Fatalf("organic Gemini cache attributed to engine: %#v", provider)
			}
		} else if provider.AttributedTokenHitRate != provider.TokenHitRate {
			t.Fatalf("causal provider attribution mismatch: %#v", provider)
		}
	}
	if report.Publishable || report.Basis != BasisSimulated {
		t.Fatalf("simulation overclaimed evidence: %#v", report)
	}
	readout := Render(report)
	for _, wanted := range []string{"evaluation: PASS", "97.79%", "publishable: false", "no provider request was sent"} {
		if !strings.Contains(readout, wanted) {
			t.Fatalf("readout missing %q:\n%s", wanted, readout)
		}
	}
}

func TestTTLExpiryFails97PercentGate(t *testing.T) {
	scenario := DefaultScenario()
	scenario.CompactionEvery = 0
	scenario.Step = 6 * time.Minute
	report, err := RunSimulated(context.Background(), cacheengine.New(cacheengine.Config{}), []ProviderConfig{DefaultProviders()[0]}, scenario, DefaultTarget())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	provider := report.Providers[0]
	if provider.GatePassed || provider.RequestHitRate != 0 || provider.ColdWrites != scenario.Turns {
		t.Fatalf("provider = %#v", provider)
	}
	if provider.OpportunityRequestCaptureRate != 0 || provider.OpportunityTokenCaptureRate != 0 {
		t.Fatalf("expired TTL credited opportunity capture: %#v", provider)
	}
	if len(provider.BlockingReasons) == 0 {
		t.Fatal("missing failure explanation")
	}
}

func TestProviderObservedReplayRejectsCausalLabelWithoutOptimizerEvidence(t *testing.T) {
	records := observedOpenAIRecords(100, true)
	records[20].OptimizerIDs = nil
	report, err := EvaluateObserved(records, DefaultTarget())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.Providers[0].GatePassed || report.Providers[0].InvalidSamples != 1 {
		t.Fatalf("provider = %#v", report.Providers[0])
	}
}

func TestNewObservationRecordBindsEngineEvidence(t *testing.T) {
	result := cacheengine.NativeResult{
		Applied: true, Decision: cacheengine.DecisionApply, Reason: cacheengine.ReasonApplied,
		Profile:      cacheengine.Profile{ID: "openai-gpt-5.6-explicit-v1", Provider: "openai", Attribution: cacheengine.AttributionCausal},
		OptimizerIDs: []string{cacheengine.OpenAIKeyOptimizerID, cacheengine.OpenAIExplicitOptimizerID},
	}
	body := []byte(`{"model":"gpt-5.6"}`)
	providerEvidence := []byte(`{"id":"response-1","usage":{"input_tokens_details":{"cached_tokens":1900,"cache_write_tokens":0}}}`)
	evidence := []byte(`{"suite":"fixture","passed":true}`)
	record := NewObservationRecord("request-1", "openai", "epoch-1", 2000, body, providerEvidence, result, TaskVerification{Passed: true, Verifier: "fixture@v1", Evidence: evidence}, []byte(`{"input_tokens_details":{"cached_tokens":1900,"cache_write_tokens":0}}`))
	result.OptimizerIDs[0] = "mutated"
	if record.Schema != ObservationSchema || !record.CacheEligible || record.EngineDecision != cacheengine.DecisionApply || record.EngineReason != cacheengine.ReasonApplied || record.ProfileID == "" || record.RequestBodySHA256 != bodyDigest(body) || record.ProviderEvidenceSHA256 != bodyDigest(providerEvidence) || !record.Applied || record.OptimizerIDs[0] != cacheengine.OpenAIKeyOptimizerID || !record.QualityPassed || record.QualityVerifier != "fixture@v1" || record.QualityEvidenceSHA256 != bodyDigest(evidence) {
		t.Fatalf("record = %#v", record)
	}
}

func TestProviderObservedReplayRetainsBelowMinimumPopulation(t *testing.T) {
	records := observedOpenAIRecords(100, true)
	records[0].CacheEligible = false
	records[0].Applied = false
	records[0].EngineDecision = cacheengine.DecisionPassThrough
	records[0].EngineReason = cacheengine.ReasonBelowMinimum
	records[0].Attribution = cacheengine.AttributionCausal
	records[0].OptimizerIDs = nil
	records[0].Usage = json.RawMessage(`{"input_tokens_details":{}}`)
	report, err := EvaluateObserved(records, Target{RequestHitRate: 0, TokenHitRate: 0, MinEligibleRequest: 99})
	if err != nil {
		t.Fatal(err)
	}
	provider := report.Providers[0]
	if !provider.GatePassed || provider.EvaluatedRequests != 100 || provider.EligibleRequests != 99 || provider.IneligibleRequests != 1 || provider.InvalidSamples != 0 {
		t.Fatalf("provider = %#v", provider)
	}
	records[0].EngineReason = cacheengine.ReasonMalformedRequest
	report, err = EvaluateObserved(records, Target{RequestHitRate: 0, TokenHitRate: 0, MinEligibleRequest: 99})
	if err != nil || report.Providers[0].InvalidSamples != 1 {
		t.Fatalf("malformed ineligible decision accepted: %#v, err=%v", report.Providers[0], err)
	}
}

func TestModelVisibleEquivalentAcceptsMetadataAndRejectsSemanticMutation(t *testing.T) {
	original := []byte(`{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}]}`)
	metadataOnly := []byte(`{"model":"gpt-5.6","prompt_cache_key":"0123456789abcdef0123456789abcdef","messages":[{"role":"user","content":[{"type":"text","text":"hello","prompt_cache_breakpoint":{"mode":"explicit"}}]}],"prompt_cache_options":{"mode":"explicit"}}`)
	mutated := bytes.Replace(metadataOnly, []byte(`"hello"`), []byte(`"changed"`), 1)
	if !ModelVisibleEquivalent(original, metadataOnly) {
		t.Fatal("cache metadata rejected as semantic change")
	}
	if ModelVisibleEquivalent(original, mutated) {
		t.Fatal("semantic mutation accepted")
	}
}

func TestModelVisibleEquivalentRejectsCacheLikeSemanticDataAndMalformedMetadata(t *testing.T) {
	original := []byte(`{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","cache_control":{"type":"semantic"}}}}]}`)
	semanticMutation := bytes.Replace(original, []byte(`"semantic"`), []byte(`"ephemeral"`), 1)
	if ModelVisibleEquivalent(original, semanticMutation) {
		t.Fatal("cache-like tool-schema mutation accepted")
	}
	malformedMetadata := []byte(`{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","cache_control":{"type":"semantic"}}}}],"prompt_cache_options":{"mode":"invented"}}`)
	if ModelVisibleEquivalent(original, malformedMetadata) {
		t.Fatal("malformed cache metadata accepted")
	}
	if ModelVisibleEquivalent([]byte(`{"value":[]}`), []byte(`{"value":[{}]}`)) {
		t.Fatal("semantic empty object discarded")
	}
	if ModelVisibleEquivalent([]byte(`{"value":1}`), []byte(`{"value":0,"value":1}`)) {
		t.Fatal("duplicate-key transform accepted")
	}
	nestedOriginal := []byte(`{"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"content":{"const":"hello"},"messages":{"type":"array"}}}}}]}`)
	nestedBlockMutation := []byte(`{"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"content":{"const":[{"type":"text","text":"hello"}]},"messages":{"type":"array"}}}}}]}`)
	if ModelVisibleEquivalent(nestedOriginal, nestedBlockMutation) {
		t.Fatal("nested semantic content block mutation accepted")
	}
	nestedMetadataMutation := []byte(`{"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"messages":{"items":{"properties":{"content":{"items":{"prompt_cache_breakpoint":{"mode":"explicit"}}}}}}}}}}]}`)
	if ModelVisibleEquivalent([]byte(`{"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"messages":{"items":{"properties":{"content":{"items":{}}}}}}}}]}`), nestedMetadataMutation) {
		t.Fatal("nested cache-like semantic field stripped as provider metadata")
	}
}

func TestOpenAISimulationStopsAtLastExplicitBreakpoint(t *testing.T) {
	engine := cacheengine.New(cacheengine.Config{})
	body := []byte(`{"model":"gpt-5.6","messages":[{"role":"system","content":"stable"},{"role":"user","content":"task"},{"role":"assistant","content":null,"tool_calls":[{"id":"call","type":"function","function":{"name":"read","arguments":"{}"}}]}]}`)
	native := cacheengine.NativeRequest{
		Scope: "test", Epoch: "epoch", PartitionKey: "session", Provider: "openai", Model: "gpt-5.6",
		Endpoint: "/v1/chat/completions", Body: body, PrefixTokens: 5000, ExpectedCalls: 2,
		RuntimeMode: "optimize", AuthMode: "payg",
	}
	result, err := engine.Optimize(context.Background(), native)
	if err != nil || !result.Applied {
		t.Fatalf("optimize = %#v, err=%v", result, err)
	}
	request := TraceRequest{Native: native, StableSegmentCount: 1}
	prefix := []PrefixSegment{{ID: "system", Tokens: 2000}, {ID: "user", Tokens: 100}, {ID: "assistant-tool", Tokens: 50}}
	lookup := simulatedLookupPrefix(request, result, prefix)
	if len(lookup) != 2 {
		t.Fatalf("lookup prefix = %#v, want through user breakpoint", lookup)
	}
}

func TestSimulationDoesNotInferCrossPartitionReuse(t *testing.T) {
	trace, err := GenerateTrace(DefaultProviders()[1], DefaultScenario())
	if err != nil {
		t.Fatal(err)
	}
	first := trace.Requests[0]
	first.Native.ExpectedRequestsPerMinute = 1
	second := first
	second.ID = "cross-partition-second"
	second.At = first.At.Add(time.Second)
	second.Native.Epoch = "agent-epoch-2"
	second.Native.PartitionKey = "agent-session-2"
	trace.Requests = []TraceRequest{first, second}
	target := Target{RequestHitRate: 0, TokenHitRate: 0, MinEligibleRequest: 1}

	isolated, err := EvaluateTrace(context.Background(), cacheengine.New(cacheengine.Config{}), trace, target)
	if err != nil {
		t.Fatal(err)
	}
	if isolated.RequestHits != 0 || isolated.ColdWrites != 2 {
		t.Fatalf("ungrounded cross-partition reuse credited: %#v", isolated)
	}

	trace.AssumeCrossPartitionReuse = true
	shared, err := EvaluateTrace(context.Background(), cacheengine.New(cacheengine.Config{}), trace, target)
	if err != nil {
		t.Fatal(err)
	}
	if shared.RequestHits != 1 || shared.ColdWrites != 1 {
		t.Fatalf("explicit cross-partition reuse not modeled: %#v", shared)
	}
}

func TestTraceJSONLContainsReplayableRealAgentRequests(t *testing.T) {
	scenario := DefaultScenario()
	scenario.Turns = 2
	scenario.CompactionEvery = 0
	trace, err := GenerateTrace(DefaultProviders()[1], scenario)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var output bytes.Buffer
	if err := WriteTraceJSONL(&output, trace); err != nil {
		t.Fatalf("write: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d", len(lines))
	}
	var record TraceRecord
	if err := json.Unmarshal([]byte(lines[1]), &record); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if record.Schema != TraceSchema || record.Provider != "openai" || record.RequestID == "" || len(record.Prefix) < 5 || !json.Valid(record.Body) || record.DeclaredInputTokens < record.PrefixTokens || record.MaxOutputTokens != 256 || !requestBudgetMatchesBody(record) {
		t.Fatalf("record = %#v", record)
	}
	parsed, err := ReadTraceJSONL(strings.NewReader(output.String()))
	if err != nil || len(parsed) != 2 || parsed[1].BodySHA256 != bodyDigest(parsed[1].Body) {
		t.Fatalf("parsed = %#v, err=%v", parsed, err)
	}
	missingScope := strings.Replace(output.String(), `"scope":"cachebench/real-agent"`, `"scope":""`, 1)
	if _, err := ReadTraceJSONL(strings.NewReader(missingScope)); err == nil {
		t.Fatal("incomplete trace identity accepted")
	}
}

func TestTraceV3BindsBudgetAcrossBuiltInProviders(t *testing.T) {
	scenario := DefaultScenario()
	scenario.Turns = 2
	scenario.CompactionEvery = 0
	for _, provider := range DefaultProviders() {
		t.Run(provider.Provider, func(t *testing.T) {
			trace, err := GenerateTrace(provider, scenario)
			if err != nil {
				t.Fatal(err)
			}
			var artifact bytes.Buffer
			if err := WriteTraceJSONL(&artifact, trace); err != nil {
				t.Fatal(err)
			}
			records, err := ReadTraceJSONL(&artifact)
			if err != nil || len(records) != 2 {
				t.Fatalf("records=%#v err=%v", records, err)
			}
			for _, record := range records {
				if record.Schema != TraceSchema || record.DeclaredInputTokens < record.PrefixTokens || record.MaxOutputTokens != 256 || !requestBudgetMatchesBody(record) {
					t.Fatalf("record=%#v", record)
				}
			}
		})
	}
}

func TestWriteJSONDoesNotMutateReportDetails(t *testing.T) {
	report := Report{Providers: []ProviderReport{{Requests: []RequestResult{{RequestID: "one"}}}}}
	var output bytes.Buffer
	if err := WriteJSON(&output, report, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(report.Providers[0].Requests) != 1 {
		t.Fatal("writer mutated caller report")
	}
}

func TestProviderObservedReplayClearsGateWithoutMintingStrongerEvidence(t *testing.T) {
	records := observedOpenAIRecords(100, true)
	report, err := EvaluateObserved(records, DefaultTarget())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	provider := report.Providers[0]
	if !provider.GatePassed || provider.RequestHitRate != 0.99 || provider.TokenHitRate < 0.98 || provider.AttributedTokenHitRate != provider.TokenHitRate {
		t.Fatalf("provider = %#v", provider)
	}
	if report.Basis != BasisObserved || report.Publishable {
		t.Fatalf("observed report overclaimed: %#v", report)
	}
}

func TestProviderObservedReplayFailsOnQualityOrImpossibleCounters(t *testing.T) {
	records := observedOpenAIRecords(100, true)
	records[50].QualityPassed = false
	records[75].Usage = json.RawMessage(`{"input_tokens_details":{"cached_tokens":10001,"cache_write_tokens":0}}`)
	records[76].Usage = json.RawMessage(`{"input_tokens_details":{"cached_tokens":6000,"cache_write_tokens":5000}}`)
	report, err := EvaluateObserved(records, DefaultTarget())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	provider := report.Providers[0]
	if provider.GatePassed || provider.InvalidSamples != 2 || provider.QualityPassRate != 0.99 {
		t.Fatalf("provider = %#v", provider)
	}
}

func TestProviderObservedReplayRejectsCounterSumOverflow(t *testing.T) {
	record := observedOpenAIRecords(1, true)[0]
	maximum := int(^uint(0) >> 1)
	record.EligibleInputTokens = maximum
	record.Usage = json.RawMessage(fmt.Sprintf(`{"input_tokens_details":{"cached_tokens":%d,"cache_write_tokens":%d}}`, maximum, maximum))
	report, err := EvaluateObserved([]ObservationRecord{record}, Target{RequestHitRate: 0, TokenHitRate: 0, MinEligibleRequest: 1})
	if err != nil || report.Overall.InvalidSamples != 1 || report.Overall.GatePassed {
		t.Fatalf("report=%#v err=%v", report.Overall, err)
	}
}

func TestProviderObservedTraceJoinRejectsOmissionsAndBodyMismatch(t *testing.T) {
	records := observedOpenAIRecords(100, true)
	trace := make([]TraceRecord, 0, len(records))
	for index := range records {
		body := []byte(fmt.Sprintf(`{"request":%d}`, index+1))
		digest := bodyDigest(body)
		records[index].RequestBodySHA256 = digest
		trace = append(trace, TraceRecord{
			Schema: TraceSchema, RequestID: records[index].RequestID, Provider: "openai",
			Epoch: records[index].Epoch, Body: body, BodySHA256: digest,
			Prefix: []PrefixSegment{{ID: "prefix", Tokens: 10000}}, StableSegmentCount: 1,
		})
	}
	report, err := EvaluateObservedAgainstTrace(records, trace, DefaultTarget())
	if err != nil || !report.Overall.GatePassed {
		t.Fatalf("report = %#v, err=%v", report.Overall, err)
	}
	if _, err := EvaluateObservedAgainstTrace(records[:99], trace, DefaultTarget()); err == nil {
		t.Fatal("omitted observation population accepted")
	}
	trace[20].Body = json.RawMessage(`{"request":"mutated"}`)
	if _, err := EvaluateObservedAgainstTrace(records, trace, DefaultTarget()); err == nil {
		t.Fatal("trace body/digest mismatch accepted")
	}
	trace[20].Body = json.RawMessage(`{"request":21}`)
	records[20].RequestBodySHA256 = strings.Repeat("0", 64)
	if _, err := EvaluateObservedAgainstTrace(records, trace, DefaultTarget()); err == nil {
		t.Fatal("request body mismatch accepted")
	}
}

func TestObservationJSONLRejectsDuplicatesUnknownFieldsAndTrailingJSON(t *testing.T) {
	record := observedOpenAIRecords(1, true)[0]
	raw, _ := json.Marshal(record)
	if _, err := ReadObservationJSONL(strings.NewReader(string(raw) + "\n" + string(raw))); err == nil {
		t.Fatal("duplicate request accepted")
	}
	unknown := strings.TrimSuffix(string(raw), "}") + `,"invented":true}`
	if _, err := ReadObservationJSONL(strings.NewReader(unknown)); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := ReadObservationJSONL(strings.NewReader(string(raw) + ` {}`)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	duplicateKey := strings.Replace(string(raw), `{"schema":`, `{"schema":"duplicate","schema":`, 1)
	if _, err := ReadObservationJSONL(strings.NewReader(duplicateKey)); err == nil {
		t.Fatal("duplicate JSON key accepted")
	}
	record.QualityVerifier = ""
	raw, _ = json.Marshal(record)
	if _, err := ReadObservationJSONL(bytes.NewReader(raw)); err == nil {
		t.Fatal("missing quality provenance accepted")
	}
	record = observedOpenAIRecords(1, true)[0]
	record.ProviderEvidenceSHA256 = ""
	raw, _ = json.Marshal(record)
	if _, err := ReadObservationJSONL(bytes.NewReader(raw)); err == nil {
		t.Fatal("missing provider evidence accepted")
	}
}

func TestObservationReaderEnforcesRecordLimit(t *testing.T) {
	records := observedOpenAIRecords(2, true)
	var raw bytes.Buffer
	for _, record := range records {
		if err := json.NewEncoder(&raw).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	limits := DefaultObservationReadLimits()
	limits.MaxRecords = 1
	if _, err := ReadObservationJSONLWithLimits(&raw, limits); err == nil {
		t.Fatal("observation record limit ignored")
	}
}

func FuzzObservationJSONLFailClosed(f *testing.F) {
	record := observedOpenAIRecords(1, true)[0]
	raw, _ := json.Marshal(record)
	f.Add(raw)
	f.Add([]byte(`{"schema":`))
	f.Add([]byte{0, 1, 2, 3})
	f.Fuzz(func(t *testing.T, raw []byte) {
		records, err := ReadObservationJSONL(bytes.NewReader(raw))
		if err != nil {
			return
		}
		if len(records) == 0 {
			t.Fatal("successful parse returned empty population")
		}
	})
}

func BenchmarkEvaluateOpenAIAgentTrace(b *testing.B) {
	scenario := DefaultScenario()
	scenario.Turns = 32
	scenario.CompactionEvery = 16
	trace, err := GenerateTrace(DefaultProviders()[1], scenario)
	if err != nil {
		b.Fatal(err)
	}
	target := Target{RequestHitRate: 0, TokenHitRate: 0, MinEligibleRequest: 1}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := EvaluateTrace(context.Background(), cacheengine.New(cacheengine.Config{}), trace, target); err != nil {
			b.Fatal(err)
		}
	}
}

func observedOpenAIRecords(count int, quality bool) []ObservationRecord {
	records := make([]ObservationRecord, 0, count)
	for index := 0; index < count; index++ {
		cached := 9900
		write := 100
		if index == 0 {
			cached, write = 0, 10000
		}
		records = append(records, ObservationRecord{
			Schema: ObservationSchema, RequestID: fmt.Sprintf("request-%03d", index+1),
			Provider: "openai", Epoch: "agent-epoch-1", EligibleInputTokens: 10000,
			ProviderEvidenceSHA256: bodyDigest([]byte(fmt.Sprintf("provider-response-%d", index+1))),
			CacheEligible:          true, Applied: true, EngineDecision: cacheengine.DecisionApply,
			EngineReason: cacheengine.ReasonApplied, ProfileID: "openai-gpt-5.6-explicit-v1",
			Attribution: cacheengine.AttributionCausal, QualityPassed: quality,
			QualityVerifier: "fixture-agent-grader@v1", QualityEvidenceSHA256: bodyDigest([]byte(fmt.Sprintf("quality-%d", index+1))),
			OptimizerIDs: []string{cacheengine.OpenAIKeyOptimizerID, cacheengine.OpenAIExplicitOptimizerID},
			Usage:        json.RawMessage(fmt.Sprintf(`{"input_tokens_details":{"cached_tokens":%d,"cache_write_tokens":%d}}`, cached, write)),
		})
	}
	return records
}
