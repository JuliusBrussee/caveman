package cachebench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/cacheengine"
)

func TestReadAgentCorpusLMCacheJSONLAndHFRows(t *testing.T) {
	rows := syntheticCorpusRows(3)
	var jsonl bytes.Buffer
	for _, row := range rows {
		wire := map[string]any{
			"session_id": row.SessionID, "model": row.Model, "input": row.Input,
			"output_length": row.OutputLength, "pre_gap": row.PreGap,
		}
		if err := json.NewEncoder(&jsonl).Encode(wire); err != nil {
			t.Fatal(err)
		}
	}
	metadata := CorpusMetadata{Name: "fixture", License: "CC-BY-4.0", Revision: "sha256:test"}
	corpus, err := ReadAgentCorpus(&jsonl, CorpusFormatLMCacheJSONL, metadata, DefaultCorpusLimits())
	if err != nil {
		t.Fatalf("read JSONL: %v", err)
	}
	if len(corpus.Rows) != 3 || corpus.SHA256 == "" || corpus.Metadata != metadata {
		t.Fatalf("corpus = %#v", corpus)
	}

	envelope := map[string]any{"features": []any{}, "rows": []any{map[string]any{
		"row_idx": 7,
		"row": map[string]any{
			"session_id": rows[0].SessionID, "model": rows[0].Model, "input": rows[0].Input,
			"output_length": rows[0].OutputLength, "pre_gap": rows[0].PreGap,
		},
	}}}
	rawEnvelope, _ := json.Marshal(envelope)
	hfCorpus, err := ReadAgentCorpus(bytes.NewReader(rawEnvelope), CorpusFormatHFRows, metadata, DefaultCorpusLimits())
	if err != nil {
		t.Fatalf("read HF rows: %v", err)
	}
	if len(hfCorpus.Rows) != 1 || hfCorpus.Rows[0].RowIndex != 7 {
		t.Fatalf("HF corpus = %#v", hfCorpus)
	}
}

func TestRunCorpusReplaysAllProvidersAndIncludesColdStarts(t *testing.T) {
	rows := syntheticCorpusRows(5)
	corpus := AgentCorpus{
		Metadata: CorpusMetadata{Name: "synthetic-agent-trace", License: "test-only", Revision: "fixture-v1"},
		Rows:     rows, SHA256: corpusDigest(rows),
	}
	target := Target{RequestHitRate: 0.75, TokenHitRate: 0.75, MinEligibleRequest: 5}
	report, err := RunCorpus(context.Background(), cacheengine.New(cacheengine.Config{}), corpus, DefaultProviders(), target)
	if err != nil {
		t.Fatalf("run corpus: %v", err)
	}
	if !report.Overall.GatePassed || report.Status != "pass" || report.Publishable || report.Basis != BasisCorpusSimulated {
		t.Fatalf("report = %#v", report)
	}
	if report.Corpus == nil || report.Corpus.Sessions != 1 || report.Corpus.Requests != 5 || report.Corpus.ColdStartCeilingRequestHitRate != 0.8 {
		t.Fatalf("corpus summary = %#v", report.Corpus)
	}
	if report.Corpus.StrictPrefixExtensions != 4 || report.Corpus.MutatedOrCompactedTransitions != 0 {
		t.Fatalf("prefix summary = %#v", report.Corpus)
	}
	for _, provider := range report.Providers {
		if !provider.GatePassed || provider.EligibleRequests != 5 || provider.RequestHits != 4 || provider.ColdWrites != 1 || provider.SafetyFailures != 0 || provider.InvalidSamples != 0 {
			t.Fatalf("provider %s = %#v", provider.Provider, provider)
		}
	}
	readout := Render(report)
	for _, wanted := range []string{"Corpus: synthetic-agent-trace", "cold-start ceiling 80.00%", "benchmark_public_corpus_simulated"} {
		if !strings.Contains(readout, wanted) {
			t.Fatalf("readout missing %q:\n%s", wanted, readout)
		}
	}
}

func TestBuildCorpusTraceExportsReplayableProviderRequests(t *testing.T) {
	rows := syntheticCorpusRows(3)
	corpus := AgentCorpus{Metadata: CorpusMetadata{Name: "export"}, Rows: rows, SHA256: corpusDigest(rows)}
	trace, err := BuildCorpusTrace(DefaultProviders()[1], corpus)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Requests) != len(rows) || trace.Provider.Provider != "openai" || trace.AssumeCrossPartitionReuse {
		t.Fatalf("trace = %#v", trace)
	}
	var output bytes.Buffer
	if err := WriteTraceJSONL(&output, trace); err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadTraceJSONL(&output)
	if err != nil || len(decoded) != len(rows) || decoded[0].BodySHA256 == "" {
		t.Fatalf("decoded = %#v, err=%v", decoded, err)
	}
	native, err := decoded[0].NativeRequest()
	if err != nil || decoded[0].Schema != TraceSchema || native.PartitionKey == "" || native.ExpectedCalls != 2 || native.RuntimeMode != "optimize" || native.AuthMode != "payg" {
		t.Fatalf("replayed native = %#v, record=%#v, err=%v", native, decoded[0], err)
	}
	legacy := decoded[0]
	legacy.Schema = TraceSchemaV1
	if _, err := legacy.NativeRequest(); err == nil {
		t.Fatal("legacy trace accepted for exact replay")
	}
	legacy.Schema = TraceSchemaV2
	if _, err := legacy.NativeRequest(); err != nil {
		t.Fatalf("v2 trace no longer reconstructs optimizer request: %v", err)
	}
	duplicateKey := strings.Replace(output.String(), `{"schema":`, `{"schema":"duplicate","schema":`, 1)
	if _, err := ReadTraceJSONL(strings.NewReader(duplicateKey)); err == nil {
		t.Fatal("duplicate trace JSON key accepted")
	}
}

func TestCorpusJSONLRejectsDuplicateKeys(t *testing.T) {
	row := syntheticCorpusRows(1)[0]
	wire := map[string]any{"session_id": row.SessionID, "model": row.Model, "input": row.Input, "output_length": row.OutputLength, "pre_gap": row.PreGap}
	raw, _ := json.Marshal(wire)
	duplicate := strings.Replace(string(raw), `"model":`, `"model":"duplicate","model":`, 1)
	if _, err := ReadAgentCorpus(strings.NewReader(duplicate), CorpusFormatLMCacheJSONL, CorpusMetadata{Name: "duplicate"}, DefaultCorpusLimits()); err == nil {
		t.Fatal("duplicate corpus JSON key accepted")
	}
}

func TestRunCorpusStartsNewEpochOnStableInitialPromptMutation(t *testing.T) {
	rows := syntheticCorpusRows(5)
	rows[3].Input[0].Content = json.RawMessage(`"changed stable policy"`)
	corpus := AgentCorpus{Metadata: CorpusMetadata{Name: "mutated"}, Rows: rows, SHA256: corpusDigest(rows)}
	target := Target{RequestHitRate: 0, TokenHitRate: 0, MinEligibleRequest: 1}
	report, err := RunCorpus(context.Background(), cacheengine.New(cacheengine.Config{}), corpus, []ProviderConfig{DefaultProviders()[1]}, target)
	if err != nil {
		t.Fatalf("run corpus: %v", err)
	}
	provider := report.Providers[0]
	if !report.Overall.GatePassed || provider.InvalidSamples != 0 || report.Corpus.MutatedOrCompactedTransitions == 0 {
		t.Fatalf("epoch transition failed: report=%#v corpus=%#v", provider, report.Corpus)
	}
	if provider.Requests[2].Epoch == provider.Requests[3].Epoch {
		t.Fatalf("stable mutation reused epoch: %#v", provider.Requests)
	}
}

func TestRunCorpusClassifiesBelowMinimumRequestsAsIneligible(t *testing.T) {
	rows := []CorpusRow{
		{RowIndex: 0, SessionID: "gaia__short", Model: "fixture", Input: []CorpusMessage{{Role: "user", Content: mustRawString("short request")}}, OutputLength: 8, PreGap: 0},
		{RowIndex: 1, SessionID: "gaia__short", Model: "fixture", Input: []CorpusMessage{{Role: "user", Content: mustRawString("short request")}, {Role: "assistant", Content: mustRawString("short answer")}, {Role: "user", Content: mustRawString("continue")}}, OutputLength: 8, PreGap: 1},
	}
	corpus := AgentCorpus{Metadata: CorpusMetadata{Name: "short"}, Rows: rows, SHA256: corpusDigest(rows)}
	report, err := RunCorpus(context.Background(), cacheengine.New(cacheengine.Config{}), corpus, []ProviderConfig{DefaultProviders()[1]}, Target{RequestHitRate: 0.97, TokenHitRate: 0.97, MinEligibleRequest: 1})
	if err != nil {
		t.Fatalf("run corpus: %v", err)
	}
	provider := report.Providers[0]
	if provider.EvaluatedRequests != 2 || provider.EligibleRequests != 0 || provider.IneligibleRequests != 2 || provider.InvalidSamples != 0 || provider.GatePassed {
		t.Fatalf("provider = %#v", provider)
	}
	if provider.QualityPassRate != 1 || len(provider.BlockingReasons) == 0 {
		t.Fatalf("provider gate = %#v", provider)
	}
}

func TestReadAgentCorpusRejectsMalformedAndLimits(t *testing.T) {
	valid := `{"session_id":"s","model":"m","input":[{"role":"user","content":"hi"}],"output_length":1,"pre_gap":0}`
	metadata := CorpusMetadata{Name: "fixture"}
	if _, err := ReadAgentCorpus(strings.NewReader(valid+` {}`), CorpusFormatLMCacheJSONL, metadata, DefaultCorpusLimits()); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	badRole := strings.Replace(valid, `"role":"user"`, `"role":"root"`, 1)
	if _, err := ReadAgentCorpus(strings.NewReader(badRole), CorpusFormatLMCacheJSONL, metadata, DefaultCorpusLimits()); err == nil {
		t.Fatal("unknown role accepted")
	}
	badGap := strings.Replace(valid, `"pre_gap":0`, `"pre_gap":1e300`, 1)
	if _, err := ReadAgentCorpus(strings.NewReader(badGap), CorpusFormatLMCacheJSONL, metadata, DefaultCorpusLimits()); err == nil {
		t.Fatal("overflowing pre_gap accepted")
	}
	limits := DefaultCorpusLimits()
	limits.MaxRows = 1
	if _, err := ReadAgentCorpus(strings.NewReader(valid+"\n"+strings.Replace(valid, `"session_id":"s"`, `"session_id":"s2"`, 1)), CorpusFormatLMCacheJSONL, metadata, limits); err == nil {
		t.Fatal("row limit bypassed")
	}
	if _, err := ReadAgentCorpus(strings.NewReader(`{"features":[]}`), CorpusFormatHFRows, metadata, DefaultCorpusLimits()); err == nil {
		t.Fatal("HF envelope without rows accepted")
	}
	limits = DefaultCorpusLimits()
	limits.MaxRetainedBytes = 1
	if _, err := ReadAgentCorpus(strings.NewReader(valid), CorpusFormatLMCacheJSONL, metadata, limits); err == nil {
		t.Fatal("retained byte limit bypassed")
	}
	limits = DefaultCorpusLimits()
	limits.MaxInputBytes = int64(len(valid) - 1)
	if _, err := ReadAgentCorpus(strings.NewReader(valid), CorpusFormatLMCacheJSONL, metadata, limits); err == nil {
		t.Fatal("source byte limit bypassed")
	}
	limits = DefaultCorpusLimits()
	limits.MaxRows = 1_000_001
	if _, err := ReadAgentCorpus(strings.NewReader(valid), CorpusFormatLMCacheJSONL, metadata, limits); err == nil {
		t.Fatal("hard corpus limit bypassed")
	}
	if _, err := ReadAgentCorpus(strings.NewReader(valid), CorpusFormatLMCacheJSONL, CorpusMetadata{Name: "bad\x00name"}, DefaultCorpusLimits()); err == nil {
		t.Fatal("control-character corpus metadata accepted")
	}
	duplicateArguments := `{"session_id":"s","model":"m","input":[{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"inspect","arguments":"{\\"path\\":1,\\"path\\":2}"}}]}],"output_length":1,"pre_gap":0}`
	if _, err := ReadAgentCorpus(strings.NewReader(duplicateArguments), CorpusFormatLMCacheJSONL, metadata, DefaultCorpusLimits()); err == nil {
		t.Fatal("duplicate tool arguments accepted")
	}
}

func FuzzAgentCorpusJSONLFailClosed(f *testing.F) {
	f.Add([]byte(`{"session_id":"s","model":"m","input":[{"role":"user","content":"hi"}],"output_length":1,"pre_gap":0}`))
	f.Add([]byte(`{"session_id":`))
	f.Add([]byte{0, 1, 2, 3})
	f.Fuzz(func(t *testing.T, raw []byte) {
		corpus, err := ReadAgentCorpus(bytes.NewReader(raw), CorpusFormatLMCacheJSONL, CorpusMetadata{Name: "fuzz"}, CorpusLimits{MaxRows: 8, MaxSessions: 8, MaxMessagesPerRequest: 16, MaxRowBytes: 1 << 20, MaxMessageBytes: 1 << 18})
		if err != nil {
			return
		}
		if len(corpus.Rows) == 0 || corpus.SHA256 == "" {
			t.Fatal("successful parse returned incomplete corpus")
		}
	})
}

func TestPinnedPublicCorpusResultPreservesFailedTokenGate(t *testing.T) {
	raw, err := os.ReadFile("results/lmcache-agentic-traces-2026-08-10.json")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Publishable bool `json:"publishable"`
		Source      struct {
			Shards []any `json:"shards"`
		} `json:"source"`
		Full struct {
			Requests         int     `json:"requests"`
			RequestHitRate   float64 `json:"request_hit_rate"`
			TokenHitRate     float64 `json:"eligible_token_hit_rate"`
			InvalidSamples   int     `json:"invalid_samples"`
			EquivalenceFails int     `json:"model_visible_equivalence_failures"`
			RequestCapture   float64 `json:"opportunity_request_capture_rate"`
			TokenCapture     float64 `json:"opportunity_token_capture_rate"`
			GatePassed       bool    `json:"strict_gate_passed"`
		} `json:"full_openai_gpt_5_6"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Publishable || len(result.Source.Shards) != 5 || result.Full.Requests != 24_880 || result.Full.RequestHitRate >= 0.97 || result.Full.TokenHitRate >= 0.97 || result.Full.RequestCapture != 1 || result.Full.TokenCapture != 1 || result.Full.InvalidSamples != 0 || result.Full.EquivalenceFails != 0 || result.Full.GatePassed {
		t.Fatalf("public result overclaimed or drifted: %#v", result)
	}
}

func syntheticCorpusRows(turns int) []CorpusRow {
	system := CorpusMessage{Role: "system", Content: mustRawString(strings.Repeat("stable repository policy ", 2_000))}
	history := []CorpusMessage{system, {Role: "user", Content: mustRawString("inspect repository and fix failing test")}}
	rows := make([]CorpusRow, 0, turns)
	for turn := 0; turn < turns; turn++ {
		input := append([]CorpusMessage(nil), history...)
		rows = append(rows, CorpusRow{
			RowIndex: turn, SessionID: "swebench__fixture__one", Model: "fixture-model",
			Input: input, OutputLength: 32, PreGap: 0.5,
		})
		history = append(history,
			CorpusMessage{
				Role: "assistant", Content: mustRawString("running workspace tool"),
				ToolCalls: []CorpusToolCall{{
					ID: fmt.Sprintf("call-%d", turn), Type: "function",
					Function: CorpusToolFunction{Name: "workspace", Arguments: `{}`},
				}},
			},
			CorpusMessage{Role: "tool", Name: "workspace", ToolCallID: fmt.Sprintf("call-%d", turn), Content: mustRawString(fmt.Sprintf("result %d", turn))},
			CorpusMessage{Role: "user", Content: mustRawString(fmt.Sprintf("continue turn %d", turn+1))},
		)
	}
	return rows
}

func mustRawString(value string) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}
