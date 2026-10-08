package standalone

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// goldenCase is one request through the shipped wiring and the upstream's
// answer to it.
type goldenCase struct {
	name, path, body, contentType, response string
	status                                  int
	headers                                 map[string]string
}

func goldenSSE(events ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(events); i += 2 {
		if events[i] != "" {
			b.WriteString("event: " + events[i] + "\n")
		}
		b.WriteString("data: " + events[i+1] + "\n\n")
	}
	return b.String()
}

var goldenCases = func() []goldenCase {
	turn := strings.Repeat("the gateway reads the request and keeps the cache prefix stable ", 12)
	messages := `{"model":"claude-sonnet-4-5","max_tokens":1024,STREAM"system":[{"type":"text","text":"You are terse."}],` +
		`"tools":[{"name":"read","description":"Read a file.","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"` + turn + `"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"` + turn + `"}]},` +
		`{"role":"user","content":[{"type":"text","text":"` + turn + `","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"ok"}]},` +
		`{"role":"user","content":[{"type":"text","text":"next <step>"}]}]}`
	anthropic := map[string]string{"x-api-key": "sk-ant-golden", "x-cave-session": "golden-session"}
	openai := map[string]string{"authorization": "Bearer sk-proj-golden", "x-cave-session": "golden-session"}
	return []goldenCase{
		{name: "messages", path: "/v1/messages", headers: anthropic, contentType: "application/json",
			body:     strings.Replace(messages, "STREAM", "", 1),
			response: `{"id":"msg_golden","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1200,"cache_read_input_tokens":800,"cache_creation_input_tokens":100,"output_tokens":60}}`},
		{name: "messages-stream", path: "/v1/messages", headers: anthropic, contentType: "text/event-stream",
			body: strings.Replace(messages, "STREAM", `"stream":true,`, 1),
			response: goldenSSE(
				"message_start", `{"type":"message_start","message":{"id":"msg_golden","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":1300,"cache_read_input_tokens":900,"cache_creation_input_tokens":0,"output_tokens":1}}}`,
				"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"usage error message response"}}`,
				"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" done"}}`,
				"content_block_stop", `{"type":"content_block_stop","index":0}`,
				"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":42}}`,
				"message_stop", `{"type":"message_stop"}`)},
		{name: "responses", path: "/v1/responses", headers: openai, contentType: "application/json",
			body:     `{"model":"gpt-5.5","input":[{"role":"user","content":"` + turn + `"}],"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`,
			response: `{"id":"resp_golden","object":"response","model":"gpt-5.5","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":900,"output_tokens":30,"input_tokens_details":{"cached_tokens":400},"output_tokens_details":{"reasoning_tokens":10}}}`},
		{name: "responses-stream", path: "/v1/responses", headers: openai, contentType: "text/event-stream",
			body: `{"model":"gpt-5.5","stream":true,"input":"` + turn + `"}`,
			response: goldenSSE(
				"response.created", `{"type":"response.created","response":{"id":"resp_golden","status":"in_progress","usage":null}}`,
				"response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}`,
				"response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"<done>"}`,
				"response.completed", `{"type":"response.completed","response":{"id":"resp_golden","status":"completed","service_tier":"default","usage":{"input_tokens":700,"output_tokens":12,"input_tokens_details":{"cached_tokens":0},"total_tokens":712}}}`)},
		{name: "chat-stream", path: "/v1/chat/completions", headers: openai, contentType: "text/event-stream",
			body: `{"model":"gpt-5.5","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"` + turn + `"}]}`,
			response: goldenSSE(
				"", `{"id":"chat_golden","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}],"usage":null}`,
				"", `{"id":"chat_golden","object":"chat.completion.chunk","model":"gpt-5.5","choices":[],"usage":{"prompt_tokens":500,"completion_tokens":9,"total_tokens":509,"prompt_tokens_details":{"cached_tokens":128}}}`) +
				"data: [DONE]\n\n"},
		{name: "provider-error", path: "/v1/messages", headers: anthropic, contentType: "application/json", status: http.StatusBadRequest,
			body:     strings.Replace(messages, "STREAM", "", 1),
			response: `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`},
		{name: "chatgpt", path: "/chatgpt/responses", headers: map[string]string{"authorization": "Bearer oauth-golden", "chatgpt-account-id": "acct-golden"}, contentType: "text/event-stream",
			body: `{"model":"gpt-5.5","stream":true,"input":[{"role":"user","content":"` + turn + `"}]}`,
			response: goldenSSE(
				"response.created", `{"type":"response.created","response":{"id":"resp_chatgpt","usage":null}}`,
				"response.completed", `{"type":"response.completed","response":{"id":"resp_chatgpt","usage":{"input_tokens":321,"output_tokens":7}}}`)},
	}
}()

type goldenTransport struct {
	mu   sync.Mutex
	next goldenCase
}

func (t *goldenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	_, _ = io.Copy(io.Discard, r.Body)
	t.mu.Lock()
	tc := t.next
	t.mu.Unlock()
	status := tc.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Request: r,
		Header: http.Header{"Content-Type": []string{tc.contentType}},
		Body:   io.NopCloser(strings.NewReader(tc.response))}, nil
}

// goldenVolatile are the columns that differ on every run.
var goldenVolatile = map[string]bool{"id": true, "ts": true, "request_id": true, "latency_ms": true, "ttfb_ms": true}

// goldenStoredRows serves every golden case, in order, through the shipped
// wiring with a real store and returns each stored row as column → value.
func goldenStoredRows(t *testing.T, async bool) []map[string]string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caveman.db")
	spend, err := store.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	upstream := &goldenTransport{}
	srv := New(config.Config{Mode: "record"}, spend, Options{HTTPClient: &http.Client{Transport: upstream}, AsyncRecord: async})
	for i, tc := range goldenCases {
		upstream.mu.Lock()
		upstream.next = tc
		upstream.mu.Unlock()
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("x-cave-trace-id", fmt.Sprintf("%032x", i+1))
		for name, value := range tc.headers {
			req.Header.Set(name, value)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if want := max(tc.status, http.StatusOK); rec.Code != want {
			t.Fatalf("%s: status %d, want %d: %s", tc.name, rec.Code, want, rec.Body.String())
		}
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if err := spend.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT * FROM requests ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		row := map[string]string{}
		for i, column := range columns {
			if goldenVolatile[column] {
				continue
			}
			switch value := values[i].(type) {
			case nil:
				row[column] = "NULL"
			case []byte:
				row[column] = string(value)
			default:
				row[column] = fmt.Sprint(value)
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestStoredRowsGolden pins every stored column of each golden request: the
// request and prefix hashes, the frozen-prefix evidence, provider usage, the
// local request token counts, prices and dollars. The values were taken from
// the build before rows were finished off the request path; a row finished
// inline and one finished by the async recorder must both still match them.
func TestStoredRowsGolden(t *testing.T) {
	if os.Getenv("CAVEMAN_GOLDEN_ROWS_PRINT") != "" {
		for _, row := range goldenStoredRows(t, false) {
			keys := make([]string, 0, len(row))
			for key := range row {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			var b strings.Builder
			b.WriteString("{")
			for _, key := range keys {
				fmt.Fprintf(&b, "%q: %q, ", key, row[key])
			}
			b.WriteString("},")
			t.Log(b.String())
		}
		return
	}
	for _, async := range []bool{false, true} {
		got := goldenStoredRows(t, async)
		if len(got) != len(goldenRows) {
			t.Fatalf("async=%v: %d rows, want %d", async, len(got), len(goldenRows))
		}
		for i, want := range goldenRows {
			for column, value := range want {
				if got[i][column] != value {
					t.Errorf("async=%v %s: %s = %q, want %q", async, goldenCases[i].name, column, got[i][column], value)
				}
			}
			for column := range got[i] {
				if _, ok := want[column]; !ok {
					t.Errorf("async=%v %s: column %s has no golden value", async, goldenCases[i].name, column)
				}
			}
		}
	}
}

var goldenRows = []map[string]string{
	{"agent_build_sha256": "", "agent_slug": "unlabeled-agent", "auth_mode": "payg", "basis": "inferred", "cache_boundary_known": "1", "cache_bust": "0", "cache_creation_1h_tokens": "0", "cache_creation_input_tokens": "100", "cache_epoch": "", "cache_prefix_sha256": "", "cache_status": "hit", "cached_input_tokens": "800", "compression_eligible": "0", "compression_ratio": "0", "compression_token_count_basis": "", "compression_tokens_after": "0", "compression_tokens_before": "0", "context_bill": "", "efficiency_plan_sha256": "", "endpoint": "/v1/messages", "error_code": "", "input_tokens": "2100", "label": "local", "model": "claude-sonnet-4-5", "optimization_ids": "", "output_tokens": "60", "price_cache_read_per_million": "0.3", "price_cache_write_1h_per_million": "6", "price_cache_write_per_million": "3.75", "price_input_per_million": "3", "price_output_per_million": "15", "price_reasoning_per_million": "15", "pricing_catalog_version": "2026-08-10", "pricing_known": "1", "pricing_model": "claude-sonnet-4-5", "pricing_provider": "anthropic", "provider": "anthropic", "provider_cache_component_sha256": "19b93154e364a9b142e1f0ac3d85c7f9501ec26b10ae46b5f8bb8dd2423466da,759ec21da4e1aefba71c1bb9d5f4c775ca0b3b5010764e438e4f8c84147f5c5a,e3e08ec308fd064608a2638092f45ef9798be097c7c8a77922d5a7eadba9e34e,1bc5141ce8630df584feb06fb207eb87cbdc45d489a24f752a4017385a16d5a6,03d708d3597fd360cd8d48f27cbdb86af92dce9c894eeb543bc1731867fd429d,1bc5141ce8630df584feb06fb207eb87cbdc45d489a24f752a4017385a16d5a6", "provider_cache_prefix_sha256": "1ebe6b17f710990dbed1faf5ce77af9dec92893422dfd603658ba4e59ae6e7b1", "raw_request_sha256": "d28d77c688baa3b7e412ed9431f1b279641efb24026d4a4a1e96e918eb23231e", "reasoning_tokens": "0", "recovery_handle": "", "request_bytes": "2873", "request_estimated_input_delta_usd": "0", "request_hash_complete": "1", "request_measurement_status": "measured", "request_savings_basis": "estimated_tokens_x_observed_cache_mix", "request_token_basis": "estimated_request_json_o200k_v1", "request_tokens_after": "552", "request_tokens_before": "552", "response_bytes": "263", "route_from": "claude-sonnet-4-5", "route_pool_id": "", "route_reason": "", "route_to": "claude-sonnet-4-5", "runtime_mode": "record", "savings_usd": "0", "session_correlation_basis": "explicit_header", "session_id": "golden-session", "status_code": "200", "stream": "0", "token_usage_basis": "provider_complete", "total_cost_usd": "0.005115", "trace_id": "00000000000000000000000000000001", "transform_location": "", "transform_trace": "", "transformed_request_sha256": "d28d77c688baa3b7e412ed9431f1b279641efb24026d4a4a1e96e918eb23231e", "upstream_response_id": "", "would_save_tokens": "0", "would_save_usd": "NULL"},
	{"agent_build_sha256": "", "agent_slug": "unlabeled-agent", "auth_mode": "payg", "basis": "inferred", "cache_boundary_known": "1", "cache_bust": "0", "cache_creation_1h_tokens": "0", "cache_creation_input_tokens": "0", "cache_epoch": "", "cache_prefix_sha256": "", "cache_status": "hit", "cached_input_tokens": "900", "compression_eligible": "0", "compression_ratio": "0", "compression_token_count_basis": "", "compression_tokens_after": "0", "compression_tokens_before": "0", "context_bill": "", "efficiency_plan_sha256": "", "endpoint": "/v1/messages", "error_code": "", "input_tokens": "2200", "label": "local", "model": "claude-sonnet-4-5", "optimization_ids": "", "output_tokens": "42", "price_cache_read_per_million": "0.3", "price_cache_write_1h_per_million": "6", "price_cache_write_per_million": "3.75", "price_input_per_million": "3", "price_output_per_million": "15", "price_reasoning_per_million": "15", "pricing_catalog_version": "2026-08-10", "pricing_known": "1", "pricing_model": "claude-sonnet-4-5", "pricing_provider": "anthropic", "provider": "anthropic", "provider_cache_component_sha256": "19b93154e364a9b142e1f0ac3d85c7f9501ec26b10ae46b5f8bb8dd2423466da,759ec21da4e1aefba71c1bb9d5f4c775ca0b3b5010764e438e4f8c84147f5c5a,e3e08ec308fd064608a2638092f45ef9798be097c7c8a77922d5a7eadba9e34e,1bc5141ce8630df584feb06fb207eb87cbdc45d489a24f752a4017385a16d5a6,03d708d3597fd360cd8d48f27cbdb86af92dce9c894eeb543bc1731867fd429d,1bc5141ce8630df584feb06fb207eb87cbdc45d489a24f752a4017385a16d5a6", "provider_cache_prefix_sha256": "1ebe6b17f710990dbed1faf5ce77af9dec92893422dfd603658ba4e59ae6e7b1", "raw_request_sha256": "6dce5f8937e279304122518822412afa2aff520fd8435cb66bebe7208e8d092c", "reasoning_tokens": "0", "recovery_handle": "", "request_bytes": "2887", "request_estimated_input_delta_usd": "0", "request_hash_complete": "1", "request_measurement_status": "measured", "request_savings_basis": "estimated_tokens_x_observed_cache_mix", "request_token_basis": "estimated_request_json_o200k_v1", "request_tokens_after": "556", "request_tokens_before": "556", "response_bytes": "912", "route_from": "claude-sonnet-4-5", "route_pool_id": "", "route_reason": "", "route_to": "claude-sonnet-4-5", "runtime_mode": "record", "savings_usd": "0", "session_correlation_basis": "explicit_header", "session_id": "golden-session", "status_code": "200", "stream": "1", "token_usage_basis": "provider_complete", "total_cost_usd": "0.0048", "trace_id": "00000000000000000000000000000002", "transform_location": "", "transform_trace": "", "transformed_request_sha256": "6dce5f8937e279304122518822412afa2aff520fd8435cb66bebe7208e8d092c", "upstream_response_id": "", "would_save_tokens": "0", "would_save_usd": "NULL"},
	{"agent_build_sha256": "", "agent_slug": "unlabeled-agent", "auth_mode": "payg", "basis": "inferred", "cache_boundary_known": "0", "cache_bust": "0", "cache_creation_1h_tokens": "0", "cache_creation_input_tokens": "0", "cache_epoch": "", "cache_prefix_sha256": "", "cache_status": "hit", "cached_input_tokens": "400", "compression_eligible": "0", "compression_ratio": "0", "compression_token_count_basis": "", "compression_tokens_after": "0", "compression_tokens_before": "0", "context_bill": "", "efficiency_plan_sha256": "", "endpoint": "/v1/responses", "error_code": "", "input_tokens": "900", "label": "local", "model": "gpt-5.5", "optimization_ids": "", "output_tokens": "30", "price_cache_read_per_million": "0.5", "price_cache_write_1h_per_million": "0", "price_cache_write_per_million": "0", "price_input_per_million": "5", "price_output_per_million": "30", "price_reasoning_per_million": "30", "pricing_catalog_version": "2026-07-10", "pricing_known": "1", "pricing_model": "gpt-5.5", "pricing_provider": "openai", "provider": "openai", "provider_cache_component_sha256": "", "provider_cache_prefix_sha256": "", "raw_request_sha256": "6e324a9515fb120015621dd0683c24aa7c68205fb49bc69a360628e36b6a3803", "reasoning_tokens": "10", "recovery_handle": "", "request_bytes": "901", "request_estimated_input_delta_usd": "0", "request_hash_complete": "1", "request_measurement_status": "measured", "request_savings_basis": "estimated_tokens_x_observed_cache_mix", "request_token_basis": "estimated_request_json_o200k_v1", "request_tokens_after": "172", "request_tokens_before": "172", "response_bytes": "276", "route_from": "gpt-5.5", "route_pool_id": "", "route_reason": "", "route_to": "gpt-5.5", "runtime_mode": "record", "savings_usd": "0", "session_correlation_basis": "explicit_header", "session_id": "golden-session", "status_code": "200", "stream": "0", "token_usage_basis": "provider_complete", "total_cost_usd": "0.0036", "trace_id": "00000000000000000000000000000003", "transform_location": "", "transform_trace": "", "transformed_request_sha256": "6e324a9515fb120015621dd0683c24aa7c68205fb49bc69a360628e36b6a3803", "upstream_response_id": "", "would_save_tokens": "0", "would_save_usd": "NULL"},
	{"agent_build_sha256": "", "agent_slug": "unlabeled-agent", "auth_mode": "payg", "basis": "inferred", "cache_boundary_known": "0", "cache_bust": "0", "cache_creation_1h_tokens": "0", "cache_creation_input_tokens": "0", "cache_epoch": "", "cache_prefix_sha256": "", "cache_status": "miss", "cached_input_tokens": "0", "compression_eligible": "0", "compression_ratio": "0", "compression_token_count_basis": "", "compression_tokens_after": "0", "compression_tokens_before": "0", "context_bill": "", "efficiency_plan_sha256": "", "endpoint": "/v1/responses", "error_code": "", "input_tokens": "700", "label": "local", "model": "gpt-5.5", "optimization_ids": "", "output_tokens": "12", "price_cache_read_per_million": "0.5", "price_cache_write_1h_per_million": "0", "price_cache_write_per_million": "0", "price_input_per_million": "5", "price_output_per_million": "30", "price_reasoning_per_million": "30", "pricing_catalog_version": "2026-07-10", "pricing_known": "1", "pricing_model": "gpt-5.5", "pricing_provider": "openai", "provider": "openai", "provider_cache_component_sha256": "", "provider_cache_prefix_sha256": "", "raw_request_sha256": "664dd93633430c9da910412cd61a79f4e28dcaac0cb3ea8fc922df1f2773e3d8", "reasoning_tokens": "0", "recovery_handle": "", "request_bytes": "812", "request_estimated_input_delta_usd": "0", "request_hash_complete": "1", "request_measurement_status": "measured", "request_savings_basis": "estimated_tokens_x_observed_cache_mix", "request_token_basis": "estimated_request_json_o200k_v1", "request_tokens_after": "149", "request_tokens_before": "149", "response_bytes": "672", "route_from": "gpt-5.5", "route_pool_id": "", "route_reason": "", "route_to": "gpt-5.5", "runtime_mode": "record", "savings_usd": "0", "session_correlation_basis": "explicit_header", "session_id": "golden-session", "status_code": "200", "stream": "1", "token_usage_basis": "provider_complete", "total_cost_usd": "0.00386", "trace_id": "00000000000000000000000000000004", "transform_location": "", "transform_trace": "", "transformed_request_sha256": "664dd93633430c9da910412cd61a79f4e28dcaac0cb3ea8fc922df1f2773e3d8", "upstream_response_id": "", "would_save_tokens": "0", "would_save_usd": "NULL"},
	{"agent_build_sha256": "", "agent_slug": "unlabeled-agent", "auth_mode": "payg", "basis": "inferred", "cache_boundary_known": "0", "cache_bust": "0", "cache_creation_1h_tokens": "0", "cache_creation_input_tokens": "0", "cache_epoch": "", "cache_prefix_sha256": "", "cache_status": "hit", "cached_input_tokens": "128", "compression_eligible": "0", "compression_ratio": "0", "compression_token_count_basis": "", "compression_tokens_after": "0", "compression_tokens_before": "0", "context_bill": "", "efficiency_plan_sha256": "", "endpoint": "/v1/chat/completions", "error_code": "", "input_tokens": "500", "label": "local", "model": "gpt-5.5", "optimization_ids": "", "output_tokens": "9", "price_cache_read_per_million": "0.5", "price_cache_write_1h_per_million": "0", "price_cache_write_per_million": "0", "price_input_per_million": "5", "price_output_per_million": "30", "price_reasoning_per_million": "30", "pricing_catalog_version": "2026-07-10", "pricing_known": "1", "pricing_model": "gpt-5.5", "pricing_provider": "openai", "provider": "openai", "provider_cache_component_sha256": "", "provider_cache_prefix_sha256": "", "raw_request_sha256": "075716ff82c8a4dd43ba1c35cafe7953ebbe03db35b11de8f06fe2807a55d76f", "reasoning_tokens": "0", "recovery_handle": "", "request_bytes": "883", "request_estimated_input_delta_usd": "0", "request_hash_complete": "1", "request_measurement_status": "measured", "request_savings_basis": "estimated_tokens_x_observed_cache_mix", "request_token_basis": "estimated_request_json_o200k_v1", "request_tokens_after": "165", "request_tokens_before": "165", "response_bytes": "407", "route_from": "gpt-5.5", "route_pool_id": "", "route_reason": "", "route_to": "gpt-5.5", "runtime_mode": "record", "savings_usd": "0", "session_correlation_basis": "explicit_header", "session_id": "golden-session", "status_code": "200", "stream": "1", "token_usage_basis": "provider_complete", "total_cost_usd": "0.002194", "trace_id": "00000000000000000000000000000005", "transform_location": "", "transform_trace": "", "transformed_request_sha256": "075716ff82c8a4dd43ba1c35cafe7953ebbe03db35b11de8f06fe2807a55d76f", "upstream_response_id": "", "would_save_tokens": "0", "would_save_usd": "NULL"},
	{"agent_build_sha256": "", "agent_slug": "unlabeled-agent", "auth_mode": "payg", "basis": "inferred", "cache_boundary_known": "1", "cache_bust": "0", "cache_creation_1h_tokens": "0", "cache_creation_input_tokens": "0", "cache_epoch": "", "cache_prefix_sha256": "", "cache_status": "unknown", "cached_input_tokens": "0", "compression_eligible": "0", "compression_ratio": "0", "compression_token_count_basis": "", "compression_tokens_after": "0", "compression_tokens_before": "0", "context_bill": "", "efficiency_plan_sha256": "", "endpoint": "/v1/messages", "error_code": "provider_400", "input_tokens": "0", "label": "local", "model": "claude-sonnet-4-5", "optimization_ids": "", "output_tokens": "0", "price_cache_read_per_million": "0", "price_cache_write_1h_per_million": "0", "price_cache_write_per_million": "0", "price_input_per_million": "0", "price_output_per_million": "0", "price_reasoning_per_million": "0", "pricing_catalog_version": "", "pricing_known": "0", "pricing_model": "", "pricing_provider": "", "provider": "anthropic", "provider_cache_component_sha256": "19b93154e364a9b142e1f0ac3d85c7f9501ec26b10ae46b5f8bb8dd2423466da,759ec21da4e1aefba71c1bb9d5f4c775ca0b3b5010764e438e4f8c84147f5c5a,e3e08ec308fd064608a2638092f45ef9798be097c7c8a77922d5a7eadba9e34e,1bc5141ce8630df584feb06fb207eb87cbdc45d489a24f752a4017385a16d5a6,03d708d3597fd360cd8d48f27cbdb86af92dce9c894eeb543bc1731867fd429d,1bc5141ce8630df584feb06fb207eb87cbdc45d489a24f752a4017385a16d5a6", "provider_cache_prefix_sha256": "1ebe6b17f710990dbed1faf5ce77af9dec92893422dfd603658ba4e59ae6e7b1", "raw_request_sha256": "d28d77c688baa3b7e412ed9431f1b279641efb24026d4a4a1e96e918eb23231e", "reasoning_tokens": "0", "recovery_handle": "", "request_bytes": "2873", "request_estimated_input_delta_usd": "NULL", "request_hash_complete": "1", "request_measurement_status": "failed", "request_savings_basis": "", "request_token_basis": "", "request_tokens_after": "0", "request_tokens_before": "0", "response_bytes": "73", "route_from": "claude-sonnet-4-5", "route_pool_id": "", "route_reason": "", "route_to": "claude-sonnet-4-5", "runtime_mode": "record", "savings_usd": "0", "session_correlation_basis": "explicit_header", "session_id": "golden-session", "status_code": "400", "stream": "0", "token_usage_basis": "unavailable", "total_cost_usd": "0", "trace_id": "00000000000000000000000000000006", "transform_location": "", "transform_trace": "", "transformed_request_sha256": "d28d77c688baa3b7e412ed9431f1b279641efb24026d4a4a1e96e918eb23231e", "upstream_response_id": "", "would_save_tokens": "0", "would_save_usd": "NULL"},
	{"agent_build_sha256": "", "agent_slug": "unlabeled-agent", "auth_mode": "subscription", "basis": "inferred", "cache_boundary_known": "0", "cache_bust": "0", "cache_creation_1h_tokens": "0", "cache_creation_input_tokens": "0", "cache_epoch": "", "cache_prefix_sha256": "", "cache_status": "", "cached_input_tokens": "0", "compression_eligible": "0", "compression_ratio": "0", "compression_token_count_basis": "", "compression_tokens_after": "0", "compression_tokens_before": "0", "context_bill": "", "efficiency_plan_sha256": "", "endpoint": "/responses", "error_code": "", "input_tokens": "321", "label": "local", "model": "gpt-5.5", "optimization_ids": "", "output_tokens": "7", "price_cache_read_per_million": "0.5", "price_cache_write_1h_per_million": "0", "price_cache_write_per_million": "0", "price_input_per_million": "5", "price_output_per_million": "30", "price_reasoning_per_million": "30", "pricing_catalog_version": "2026-07-10", "pricing_known": "1", "pricing_model": "gpt-5.5", "pricing_provider": "openai", "provider": "chatgpt-subscription", "provider_cache_component_sha256": "", "provider_cache_prefix_sha256": "", "raw_request_sha256": "cf8d9cf22b6ef257334caf91c610a9a9ca2eea6e23d93771b826c63967342fdb", "reasoning_tokens": "0", "recovery_handle": "", "request_bytes": "840", "request_estimated_input_delta_usd": "0", "request_hash_complete": "1", "request_measurement_status": "measured", "request_savings_basis": "estimated_tokens_x_observed_cache_mix", "request_token_basis": "estimated_request_json_o200k_v1", "request_tokens_after": "157", "request_tokens_before": "157", "response_bytes": "248", "route_from": "/chatgpt/responses", "route_pool_id": "", "route_reason": "", "route_to": "https://chatgpt.com/backend-api/codex/responses", "runtime_mode": "record", "savings_usd": "0", "session_correlation_basis": "uncorrelated", "session_id": "", "status_code": "200", "stream": "1", "token_usage_basis": "provider_complete", "total_cost_usd": "0", "trace_id": "00000000000000000000000000000007", "transform_location": "", "transform_trace": "", "transformed_request_sha256": "cf8d9cf22b6ef257334caf91c610a9a9ca2eea6e23d93771b826c63967342fdb", "upstream_response_id": "", "would_save_tokens": "0", "would_save_usd": "NULL"},
}
