package importers

import (
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/telemetry"
)

func TestLangfuseImportDropsContentAndStampsProvenance(t *testing.T) {
	data := []byte(`{"observations":[{"id":"span-1","traceId":"trace-1","type":"GENERATION","name":"answer","input":{"secret":"prompt"},"output":{"secret":"completion"}}]}`)
	rows, _, err := Parse(FormatLangfuse, data, Options{
		OrganizationID: "018f0000-0000-7000-8000-000000000001",
		ProjectID:      "018f0000-0000-7000-8000-000000000002",
		IngestionID:    "018f0000-0000-7000-8000-000000000003",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.SourceKind != "file_import" || row.SourceSystem != "langfuse" || row.IngestionID == "" {
		t.Fatalf("provenance = %+v", row)
	}
	if _, ok := row.Attributes["langfuse.input"]; ok {
		t.Fatal("langfuse input content persisted")
	}
	if _, ok := row.Attributes["langfuse.output"]; ok {
		t.Fatal("langfuse output content persisted")
	}
}

func TestOTLPImportDropsReservedAndContentAttributes(t *testing.T) {
	data := []byte(`{"resourceSpans":[{"resource":{"attributes":[]},"scopeSpans":[{"spans":[{"traceId":"trace-1","spanId":"span-1","name":"chat","attributes":[{"key":"cave.reserved.causality_depth","value":{"stringValue":"0"}},{"key":"gen_ai.input.messages","value":{"stringValue":"secret"}},{"key":"gen_ai.provider.name","value":{"stringValue":"openai"}}]}]}]}]}`)
	rows, _, err := Parse(FormatOTLP, data, Options{OrganizationID: "o", ProjectID: "p", IngestionID: "i"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Provider != "openai" {
		t.Fatalf("provider = %q, want openai", rows[0].Provider)
	}
	if _, ok := rows[0].Attributes["cave.reserved.causality_depth"]; ok {
		t.Fatal("reserved attribute persisted")
	}
	if _, ok := rows[0].Attributes["gen_ai.input.messages"]; ok {
		t.Fatal("content attribute persisted")
	}
}

func TestOTLPImportMapsOpenInferenceWithoutContent(t *testing.T) {
	data := []byte(`{"resourceSpans":[{"resource":{"attributes":[]},"scopeSpans":[{"spans":[{"traceId":"oi-trace","spanId":"oi-span","name":"completion","attributes":[{"key":"openinference.span.kind","value":{"stringValue":"LLM"}},{"key":"llm.provider","value":{"stringValue":"azure"}},{"key":"llm.system","value":{"stringValue":"openai"}},{"key":"llm.model_name","value":{"stringValue":"gpt-5.5"}},{"key":"llm.token_count.prompt","value":{"intValue":"11"}},{"key":"llm.token_count.completion","value":{"intValue":"7"}},{"key":"llm.input_messages.0.message.content","value":{"stringValue":"private"}}]}]}]}]}`)
	rows, _, err := Parse(FormatOTLP, data, Options{
		OrganizationID: "org",
		ProjectID:      "project",
		SourceKind:     telemetry.SourceKindFileImport,
		SourceSystem:   telemetry.SourceSystemPhoenix,
		IngestionID:    "import-oi",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Provider != "azure" || row.Model != "gpt-5.5" || row.SpanType != "llm" || row.InputTokens != 11 || row.OutputTokens != 7 {
		t.Fatalf("OpenInference mapping = %+v", row)
	}
	if _, ok := row.Attributes["llm.input_messages.0.message.content"]; ok {
		t.Fatal("OpenInference content survived metadata-only import")
	}
}
