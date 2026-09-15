package importers

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testOrg  = "11111111-1111-1111-1111-111111111111"
	testProj = "22222222-2222-2222-2222-222222222222"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// assertScoped verifies every produced row carries the authenticated org/project
// (never the file's) and the honest non-null defaults.
func assertScoped(t *testing.T, rows []Span) {
	t.Helper()
	for i, r := range rows {
		if r.OrganizationID != testOrg {
			t.Errorf("row %d: organization_id = %q, want %q (tenant-scoped)", i, r.OrganizationID, testOrg)
		}
		if r.ProjectID != testProj {
			t.Errorf("row %d: project_id = %q, want %q (tenant-scoped)", i, r.ProjectID, testProj)
		}
		if r.AgentSlug == "" || r.WorkflowSlug == "" {
			t.Errorf("row %d: agent/workflow slug must be non-empty, got %q/%q", i, r.AgentSlug, r.WorkflowSlug)
		}
		if r.Attributes == nil {
			t.Errorf("row %d: attributes map must be non-nil", i)
		}
		if r.EventsJSON == "" {
			t.Errorf("row %d: events_json must be non-empty", i)
		}
	}
}

func opts() Options { return Options{OrganizationID: testOrg, ProjectID: testProj} }

func TestParseOTLP(t *testing.T) {
	rows, summary, err := Parse(FormatOTLP, read(t, "otlp.json"), opts())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if summary.RowCount != 2 || len(rows) != 2 {
		t.Fatalf("row count = %d, want 2", len(rows))
	}
	assertScoped(t, rows)

	g := rows[0]
	if g.Provider != "openai" {
		t.Errorf("provider = %q, want openai", g.Provider)
	}
	if g.Model != "gpt-5.5-2026" { // response.model wins over request.model
		t.Errorf("model = %q, want gpt-5.5-2026", g.Model)
	}
	if g.SpanType != "chat" {
		t.Errorf("span_type = %q, want chat", g.SpanType)
	}
	if g.InputTokens != 1200 || g.OutputTokens != 340 || g.CachedInputTokens != 800 {
		t.Errorf("tokens = %d/%d/%d, want 1200/340/800", g.InputTokens, g.OutputTokens, g.CachedInputTokens)
	}
	if g.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace_id = %q", g.TraceID)
	}
	if g.DurationMS != 1500 {
		t.Errorf("duration_ms = %d, want 1500", g.DurationMS)
	}
	if g.AgentSlug != "billing-agent" { // from resource service.name
		t.Errorf("agent_slug = %q, want billing-agent", g.AgentSlug)
	}
	if rows[1].Status != "error" { // status code 2
		t.Errorf("row1 status = %q, want error", rows[1].Status)
	}
	if rows[1].ToolName != "web_search" {
		t.Errorf("row1 tool_name = %q, want web_search", rows[1].ToolName)
	}
}

func TestParseCavemanJSONL(t *testing.T) {
	rows, summary, err := Parse(FormatCavemanJSONL, read(t, "caveman.jsonl"), opts())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if summary.RowCount != 3 || len(rows) != 3 { // blank line skipped
		t.Fatalf("row count = %d, want 3", len(rows))
	}
	assertScoped(t, rows)

	if rows[0].Provider != "anthropic" || rows[0].Model != "claude-sonnet-4-5" {
		t.Errorf("row0 provider/model = %q/%q", rows[0].Provider, rows[0].Model)
	}
	if rows[0].InputTokens != 500 || rows[0].CachedInputTokens != 300 {
		t.Errorf("row0 tokens = %d/%d, want 500/300", rows[0].InputTokens, rows[0].CachedInputTokens)
	}
	if rows[0].TraceID != "aaaa0000bbbb1111cccc2222dddd3333" {
		t.Errorf("row0 trace_id = %q", rows[0].TraceID)
	}
	if rows[1].ToolName != "read_file" {
		t.Errorf("row1 tool_name = %q, want read_file", rows[1].ToolName)
	}
	if rows[2].Status != "error" || rows[2].Provider != "gemini" {
		t.Errorf("row2 status/provider = %q/%q", rows[2].Status, rows[2].Provider)
	}
}

func TestParseLangfuse(t *testing.T) {
	rows, summary, err := Parse(FormatLangfuse, read(t, "langfuse.json"), opts())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if summary.RowCount != 2 || len(rows) != 2 {
		t.Fatalf("row count = %d, want 2", len(rows))
	}
	assertScoped(t, rows)

	a := rows[0]
	if a.Provider != "openai" || a.Model != "gpt-5.5" {
		t.Errorf("row0 provider/model = %q/%q", a.Provider, a.Model)
	}
	if a.InputTokens != 2400 || a.OutputTokens != 600 || a.CachedInputTokens != 1800 {
		t.Errorf("row0 tokens = %d/%d/%d, want 2400/600/1800", a.InputTokens, a.OutputTokens, a.CachedInputTokens)
	}
	if a.TraceID != "lf-trace-0001" || a.SpanID != "obs-1" {
		t.Errorf("row0 ids = %q/%q", a.TraceID, a.SpanID)
	}
	if a.AgentSlug != "research-agent" || a.WorkflowSlug != "daily-digest" {
		t.Errorf("row0 agent/workflow = %q/%q", a.AgentSlug, a.WorkflowSlug)
	}
	if a.DurationMS != 2500 {
		t.Errorf("row0 duration_ms = %d, want 2500", a.DurationMS)
	}

	b := rows[1] // legacy promptTokens/completionTokens + ERROR level + parent ref
	if b.InputTokens != 1000 || b.OutputTokens != 200 {
		t.Errorf("row1 tokens = %d/%d, want 1000/200", b.InputTokens, b.OutputTokens)
	}
	if b.Status != "error" {
		t.Errorf("row1 status = %q, want error", b.Status)
	}
	if b.ParentSpanID != "obs-1" {
		t.Errorf("row1 parent = %q, want obs-1", b.ParentSpanID)
	}
}

func TestParseHelicone(t *testing.T) {
	rows, summary, err := Parse(FormatHelicone, read(t, "helicone.json"), opts())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if summary.RowCount != 2 || len(rows) != 2 {
		t.Fatalf("row count = %d, want 2", len(rows))
	}
	assertScoped(t, rows)

	a := rows[0]
	if a.Provider != "openai" || a.Model != "gpt-5.5-2026" { // response.model wins
		t.Errorf("row0 provider/model = %q/%q", a.Provider, a.Model)
	}
	if a.InputTokens != 1500 || a.OutputTokens != 450 || a.CachedInputTokens != 900 {
		t.Errorf("row0 tokens = %d/%d/%d, want 1500/450/900", a.InputTokens, a.OutputTokens, a.CachedInputTokens)
	}
	if a.TraceID != "hl-trace-1" || a.SpanID != "req-aaa" {
		t.Errorf("row0 ids = %q/%q", a.TraceID, a.SpanID)
	}
	if a.TotalCostUSD != 0.0211 {
		t.Errorf("row0 cost = %v, want 0.0211", a.TotalCostUSD)
	}
	if a.Status != "ok" {
		t.Errorf("row0 status = %q, want ok", a.Status)
	}

	b := rows[1] // 500 → error; falls back request.model/provider; trace_id from id
	if b.Status != "error" {
		t.Errorf("row1 status = %q, want error", b.Status)
	}
	if b.Provider != "anthropic" || b.Model != "claude-sonnet-4-5" {
		t.Errorf("row1 provider/model = %q/%q", b.Provider, b.Model)
	}
	if b.TraceID != "req-bbb" {
		t.Errorf("row1 trace_id = %q, want req-bbb", b.TraceID)
	}
}

func TestParseGeneric(t *testing.T) {
	o := opts()
	o.FieldMap = map[string]string{
		"trace_id":            "trace.id",
		"span_id":             "id",
		"timestamp":           "created_at",
		"end_timestamp":       "ended_at",
		"provider":            "request.provider",
		"model":               "request.model",
		"input_tokens":        "usage.prompt_tokens",
		"output_tokens":       "usage.completion_tokens",
		"cached_input_tokens": "usage.cache_read",
		"total_cost_usd":      "cost",
		"agent_slug":          "meta.agent",
	}
	rows, summary, err := Parse(FormatGeneric, read(t, "generic.json"), o)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if summary.RowCount != 2 || len(rows) != 2 {
		t.Fatalf("row count = %d, want 2", len(rows))
	}
	assertScoped(t, rows)

	a := rows[0]
	if a.TraceID != "g-trace-1" || a.SpanID != "g-span-1" {
		t.Errorf("row0 ids = %q/%q", a.TraceID, a.SpanID)
	}
	if a.Provider != "openai" || a.Model != "gpt-5.5" {
		t.Errorf("row0 provider/model = %q/%q", a.Provider, a.Model)
	}
	if a.InputTokens != 700 || a.OutputTokens != 150 || a.CachedInputTokens != 400 {
		t.Errorf("row0 tokens = %d/%d/%d, want 700/150/400", a.InputTokens, a.OutputTokens, a.CachedInputTokens)
	}
	if a.TotalCostUSD != 0.0072 {
		t.Errorf("row0 cost = %v, want 0.0072", a.TotalCostUSD)
	}
	if a.AgentSlug != "ops-agent" {
		t.Errorf("row0 agent = %q, want ops-agent", a.AgentSlug)
	}
	if a.DurationMS != 1200 {
		t.Errorf("row0 duration_ms = %d, want 1200", a.DurationMS)
	}
}

func TestHeliconeMissingStatusCodeIsNotReportedAsSuccess(t *testing.T) {
	rows, _, err := Parse(FormatHelicone, []byte(`{"data":[{"id":"req-unknown","created_at":"2026-06-01T10:00:00Z","request":{"model":"gpt-test"},"response":{}}]}`), opts())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "error" {
		t.Fatalf("unknown Helicone status rows = %+v, want one error row", rows)
	}
}

// TestUnknownFormatFailsClosed — a misspelled/unknown format is an error, never
// a silent pass-through (honesty: fail-closed).
func TestUnknownFormatFailsClosed(t *testing.T) {
	for _, f := range []string{"langfues", "otelp", "", "csv", "GENERIC"} {
		rows, _, err := Parse(f, []byte(`[]`), opts())
		if err == nil {
			t.Errorf("format %q: expected error, got nil", f)
		}
		if rows != nil {
			t.Errorf("format %q: expected no rows on error, got %d", f, len(rows))
		}
	}
}

// TestMalformedInput — a malformed body fails the import and yields NO partial
// rows (no partial silent ingest).
func TestMalformedInput(t *testing.T) {
	cases := []struct {
		format string
		body   string
	}{
		{FormatOTLP, `{"resourceSpans": [ this is not json`},
		{FormatCavemanJSONL, "{\"provider\":\"openai\"}\n{not valid json}\n"},
		{FormatLangfuse, `{"observations": "should be an array but isn't valid"x}`},
		{FormatHelicone, `{"data": [ broken`},
		{FormatGeneric, `not even json`},
	}
	for _, c := range cases {
		o := opts()
		if c.format == FormatGeneric {
			o.FieldMap = map[string]string{"model": "model"}
		}
		rows, _, err := Parse(c.format, []byte(c.body), o)
		if err == nil {
			t.Errorf("format %q: expected parse error, got nil", c.format)
		}
		if len(rows) != 0 {
			t.Errorf("format %q: expected 0 rows on malformed input, got %d (partial ingest!)", c.format, len(rows))
		}
	}
}

// TestGenericRequiresFieldMap — an empty field map fails closed (a no-op
// mapping would silently ingest blank rows).
func TestGenericRequiresFieldMap(t *testing.T) {
	rows, _, err := Parse(FormatGeneric, read(t, "generic.json"), opts())
	if err == nil {
		t.Fatal("expected error for empty field map")
	}
	if rows != nil {
		t.Errorf("expected no rows, got %d", len(rows))
	}
}

// TestGenericUnknownTargetColumn — a target column outside the spans schema is
// rejected (fail-closed), not silently dropped into nowhere.
func TestGenericUnknownTargetColumn(t *testing.T) {
	o := opts()
	o.FieldMap = map[string]string{"not_a_column": "x"}
	_, _, err := Parse(FormatGeneric, read(t, "generic.json"), o)
	if err == nil {
		t.Fatal("expected error for unknown target column")
	}
}

func TestGenericRequiresStableIdentityAndSourceTimeMappings(t *testing.T) {
	base := map[string]string{
		"trace_id":  "trace.id",
		"span_id":   "id",
		"timestamp": "created_at",
	}
	for _, missing := range []string{"trace_id", "span_id", "timestamp"} {
		t.Run(missing, func(t *testing.T) {
			o := opts()
			o.FieldMap = make(map[string]string, len(base)-1)
			for target, path := range base {
				if target != missing {
					o.FieldMap[target] = path
				}
			}
			rows, _, err := Parse(FormatGeneric, read(t, "generic.json"), o)
			if err == nil || !strings.Contains(err.Error(), `requires a "`+missing+`" mapping`) {
				t.Fatalf("missing %s mapping error = %v", missing, err)
			}
			if rows != nil {
				t.Fatalf("missing %s mapping returned %d rows", missing, len(rows))
			}
		})
	}
}

func TestGenericMissingMappedPathFailsWholeImport(t *testing.T) {
	o := opts()
	o.FieldMap = map[string]string{
		"trace_id":  "trace.id",
		"span_id":   "id",
		"timestamp": "created_at",
		"model":     "request.modle",
	}
	rows, _, err := Parse(FormatGeneric, read(t, "generic.json"), o)
	if err == nil || !strings.Contains(err.Error(), `request.modle`) {
		t.Fatalf("misspelled source path error = %v", err)
	}
	if rows != nil {
		t.Fatalf("misspelled source path returned %d partial rows", len(rows))
	}
}

func TestGenericEmptyDatasetRemainsValid(t *testing.T) {
	o := opts()
	o.FieldMap = map[string]string{
		"trace_id":  "trace.id",
		"span_id":   "id",
		"timestamp": "created_at",
	}
	rows, summary, err := Parse(FormatGeneric, []byte(`[]`), o)
	if err != nil || len(rows) != 0 || summary.RowCount != 0 {
		t.Fatalf("empty generic dataset = rows:%v summary:%+v err:%v", rows, summary, err)
	}
}

func TestGenericRejectsEmptyIdentityAndInvalidTime(t *testing.T) {
	fieldMap := map[string]string{
		"trace_id":  "trace_id",
		"span_id":   "span_id",
		"timestamp": "timestamp",
	}
	for name, body := range map[string]string{
		"empty trace":       `[{"trace_id":"","span_id":"s1","timestamp":"2026-06-01T10:00:00Z"}]`,
		"empty span":        `[{"trace_id":"t1","span_id":"","timestamp":"2026-06-01T10:00:00Z"}]`,
		"invalid timestamp": `[{"trace_id":"t1","span_id":"s1","timestamp":"not-a-time"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			o := opts()
			o.FieldMap = fieldMap
			rows, _, err := Parse(FormatGeneric, []byte(body), o)
			if err == nil {
				t.Fatalf("%s accepted as default status/current-time row", name)
			}
			if rows != nil {
				t.Fatalf("%s returned %d rows", name, len(rows))
			}
		})
	}
}

func TestFlexibleUsageNumbersFailClosed(t *testing.T) {
	for name, value := range map[string]any{
		"negative":   -1,
		"fraction":   1.5,
		"nan string": "NaN",
		"inf string": "+Inf",
		"overflow":   "18446744073709551616",
		"not number": "many",
	} {
		t.Run(name, func(t *testing.T) {
			if got := parseFlexInt(value); got != 0 {
				t.Fatalf("parseFlexInt(%v) = %d, want honest zero", value, got)
			}
		})
	}
	if got := parseFlexInt("9007199254740993"); got != 9007199254740993 {
		t.Fatalf("large exact string = %d, want 9007199254740993", got)
	}
	for name, value := range map[string]any{
		"negative":   -0.01,
		"nan string": "NaN",
		"inf string": "+Inf",
		"not number": "money",
	} {
		t.Run("cost "+name, func(t *testing.T) {
			if got := parseFlexFloat(value); got != 0 || math.Signbit(got) {
				t.Fatalf("parseFlexFloat(%v) = %v, want honest zero", value, got)
			}
		})
	}
}

func TestImportedNonFiniteCostCannotPoisonAggregates(t *testing.T) {
	sp := Span{TotalCostUSD: math.Inf(1)}
	applyScope(&sp, opts())
	if sp.TotalCostUSD != 0 {
		t.Fatalf("cost = %v, want honest zero", sp.TotalCostUSD)
	}
}

func TestRound6RejectsFiniteOverflow(t *testing.T) {
	if got := roundUSD(math.MaxFloat64); got != 0 {
		t.Fatalf("roundUSD(MaxFloat64) = %v, want honest zero", got)
	}
}

func TestRoundUSDPreservesSubMicroDollarSpend(t *testing.T) {
	if got := roundUSD(0.0000002); got != 0.0000002 {
		t.Fatalf("roundUSD = %.10f, want %.10f", got, 0.0000002)
	}
}

// TestFileOrgIgnored — caveman-jsonl carries org/project fields in the file;
// the importer must overwrite them with the authenticated scope.
func TestFileOrgIgnored(t *testing.T) {
	rows, _, err := Parse(FormatCavemanJSONL, read(t, "caveman.jsonl"), opts())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for i, r := range rows {
		if r.OrganizationID == "FILE-SHOULD-BE-OVERWRITTEN" {
			t.Errorf("row %d: file org leaked through (must be overwritten by JWT scope)", i)
		}
	}
}
