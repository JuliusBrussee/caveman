package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

type recordScenario struct {
	name, path, body string
	header           map[string]string
	config           func() Config // fresh adapters, transport and compressor per run
	exercises        func(RequestRecord) bool
}

func recordScenarios() []recordScenario {
	anthropicKey := map[string]string{"x-api-key": "sk-ant-test", "x-cave-session": "s-1"}
	messages := func(stream bool) func() Config {
		return func() Config {
			return Config{
				Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")},
				Auth:     stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}}, Creds: stubCreds{key: "sk-ant-test"},
				HTTPClient: &http.Client{Transport: benchUpstream(stream)},
			}
		}
	}
	chat := func(mode string, mcp bool, statuses ...int) func() Config {
		return func() Config {
			return Config{
				Adapters: []providers.Adapter{openai.New("https://api.openai.com")},
				Auth:     stubAuth{rc: RequestContext{Label: "local", RuntimeMode: mode}}, Creds: stubCreds{key: "sk-byok"},
				Compressor:     &stubCompressor{out: []byte("X"), before: 100, after: 40, handle: "ccr_test123"},
				RecoveryViaMCP: mcp,
				HTTPClient:     &http.Client{Transport: &captureTransport{statuses: statuses, responses: []string{chatRespBody, chatRespBody}}},
			}
		}
	}
	chatgptSSE := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":111,\"output_tokens\":22}}}\n\n"
	return []recordScenario{
		{name: "record messages", path: "/v1/messages", body: string(benchAnthropicBody(20_000, false)), header: anthropicKey, config: messages(false),
			exercises: func(row RequestRecord) bool {
				return row.RequestTokensBefore > 0 && row.ProviderCachePrefixSHA256 != "" && row.RequestEstimatedInputDeltaUSD != nil
			}},
		{name: "record messages stream", path: "/v1/messages", body: string(benchAnthropicBody(20_000, true)), header: anthropicKey, config: messages(true)},
		{name: "compress with server retrieve", path: "/v1/chat/completions", body: chatReqBody, config: chat("compress", false),
			exercises: func(row RequestRecord) bool {
				return row.RequestTokensBefore != row.RequestTokensAfter && row.RequestEstimatedInputDeltaUSD != nil && row.SavingsUSD > 0
			}},
		{name: "compress with agent recovery", path: "/v1/chat/completions", body: chatReqBody, config: chat("compress", true)},
		{name: "compress then original bytes", path: "/v1/chat/completions", body: chatReqBody, config: chat("compress", true, 429, 200)},
		{name: "upstream error", path: "/v1/chat/completions", body: chatReqBody, config: chat("record", false, 500)},
		{name: "chatgpt subscription", path: "/chatgpt/responses", body: `{"model":"gpt-5.5","stream":true,"input":[{"role":"user","content":"hello there"}]}`,
			header: map[string]string{"authorization": "Bearer oauth-test", "chatgpt-account-id": "acct"},
			config: func() Config {
				return Config{
					Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}}, Creds: stubCreds{},
					ChatGPTUpstream: "https://chatgpt.test/backend-api/codex",
					HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
						_, _ = io.Copy(io.Discard, r.Body)
						return &http.Response{StatusCode: http.StatusOK, Request: r, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
							Body: io.NopCloser(strings.NewReader(chatgptSSE))}, nil
					})},
				}
			}},
	}
}

func serveScenario(t *testing.T, sc recordScenario, async bool, times int) []RequestRecord {
	t.Helper()
	sink := &captureSink{}
	cfg := sc.config()
	cfg.Sink, cfg.AsyncRecord = sink, async
	srv := New(cfg)
	for i := 0; i < times; i++ {
		req := httptest.NewRequest(http.MethodPost, sc.path, strings.NewReader(sc.body))
		req.Header.Set("x-cave-trace-id", fmt.Sprintf("%032d", i))
		for name, value := range sc.header {
			req.Header.Set(name, value)
		}
		srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	rows := append([]RequestRecord(nil), sink.rows...)
	for i := range rows {
		// The clock and the per-request id are the only fields allowed to differ.
		rows[i].Timestamp, rows[i].RequestID, rows[i].LatencyMS, rows[i].TTFBMS = "", "", 0, 0
	}
	return rows
}

// TestAsyncRecordRowsMatchInline: a row finished off the request path is the
// row the handler used to finish itself — request token counts, booked
// compression savings, hashes and prefix evidence included.
func TestAsyncRecordRowsMatchInline(t *testing.T) {
	for _, sc := range recordScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			inline, async := serveScenario(t, sc, false, 3), serveScenario(t, sc, true, 3)
			if len(inline) != 3 {
				t.Fatalf("inline recorded %d rows, want 3", len(inline))
			}
			if sc.exercises != nil && !sc.exercises(inline[0]) {
				t.Fatalf("scenario does not reach what it is named for: %+v", inline[0])
			}
			if !reflect.DeepEqual(inline, async) {
				t.Fatalf("async rows differ:\ninline %+v\nasync  %+v", inline, async)
			}
		})
	}
}

// TestAsyncRecordKeepsRequestOrder: a large request's slow token count does
// not let the small request after it reach the sink first.
func TestAsyncRecordKeepsRequestOrder(t *testing.T) {
	sink := &captureSink{}
	srv := New(Config{
		Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")},
		Auth:     stubAuth{rc: RequestContext{RuntimeMode: "record"}}, Creds: stubCreds{key: "sk-ant-test"},
		Sink: sink, HTTPClient: &http.Client{Transport: benchUpstream(false)}, AsyncRecord: true,
	})
	var sizes []int
	for _, size := range []int{730_000, 9_000, 180_000, 9_000} {
		body := benchAnthropicBody(size, false)
		sizes = append(sizes, len(body))
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
		req.Header.Set("x-api-key", "sk-ant-test")
		srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}
	_ = srv.Close()
	var got []int
	for _, row := range sink.rows {
		got = append(got, row.RequestBytes)
		if row.RequestMeasurementStatus != "measured" {
			t.Fatalf("row not finished: %+v", row)
		}
	}
	if !reflect.DeepEqual(got, sizes) {
		t.Fatalf("sink order %v, want request order %v", got, sizes)
	}
}

// gatedBatchSink holds its first write until gate opens, so the rows
// recorded meanwhile queue up behind it.
type gatedBatchSink struct {
	gate chan struct{}
	once sync.Once
	mu   sync.Mutex
	rows []RequestRecord
}

func (s *gatedBatchSink) Record(row RequestRecord) { s.RecordBatch([]RequestRecord{row}) }

func (s *gatedBatchSink) RecordBatch(rows []RequestRecord) {
	s.once.Do(func() { <-s.gate })
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, rows...)
}

func TestAsyncRecordCloseDrainsQueuedRows(t *testing.T) {
	sink := &gatedBatchSink{gate: make(chan struct{})}
	srv := New(Config{
		Adapters: []providers.Adapter{openai.New("https://api.openai.com")},
		Auth:     stubAuth{rc: RequestContext{RuntimeMode: "record"}}, Creds: stubCreds{key: "sk-byok"},
		Sink: sink, HTTPClient: &http.Client{Transport: &captureTransport{}}, AsyncRecord: true,
	})
	serve := func(i int) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
		req.Header.Set("x-cave-trace-id", fmt.Sprintf("%032d", i))
		srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}
	for i := 0; i < 6; i++ {
		serve(i) // returns at once: the sink is still holding the first row
	}
	close(sink.gate)
	_ = srv.Close()
	if len(sink.rows) != 6 {
		t.Fatalf("Close left rows unwritten: %d of 6", len(sink.rows))
	}
	for i, row := range sink.rows {
		if row.TraceID != fmt.Sprintf("%032d", i) {
			t.Fatalf("row %d is %s: out of order", i, row.TraceID)
		}
	}
	// After Close a late request still records, inline.
	serve(6)
	if len(sink.rows) != 7 {
		t.Fatalf("a request after Close recorded %d rows, want 7", len(sink.rows))
	}
	_ = srv.Close() // twice is fine
}

// TestAsyncRecordFlushWaitsForRecordedRows: after Flush a reader of the sink
// sees every request served so far, its slow token count included.
func TestAsyncRecordFlushWaitsForRecordedRows(t *testing.T) {
	sink := &captureSink{}
	srv := New(Config{
		Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")},
		Auth:     stubAuth{rc: RequestContext{RuntimeMode: "record"}}, Creds: stubCreds{key: "sk-ant-test"},
		Sink: sink, HTTPClient: &http.Client{Transport: benchUpstream(false)}, AsyncRecord: true,
	})
	defer srv.Close()
	for want := 1; want <= 2; want++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(benchAnthropicBody(730_000, false))))
		req.Header.Set("x-api-key", "sk-ant-test")
		srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
		srv.Flush()
		sink.mu.Lock()
		got := len(sink.rows)
		finished := got > 0 && sink.rows[got-1].RequestMeasurementStatus == "measured"
		sink.mu.Unlock()
		if got != want || !finished {
			t.Fatalf("after Flush the sink holds %d rows (last finished: %v), want %d", got, finished, want)
		}
	}
}

// TestRecorderSurvivesPanics: a panic while finishing or writing rows drops
// those rows, as a panicking handler did, and neither stops the proxy nor
// the rows after them.
func TestRecorderSurvivesPanics(t *testing.T) {
	var written []string
	writes := 0
	r := newRecorder(func(rows []finishedRecord) {
		if writes++; writes == 1 {
			panic("sink failed")
		}
		for _, row := range rows {
			written = append(written, row.row.RequestID)
		}
	}, nil)
	keep := func(*RequestRecord) {}
	r.submit(RequestRecord{RequestID: "lost-in-write"}, false, keep)
	r.flush()
	r.submit(RequestRecord{RequestID: "a"}, false, keep)
	r.submit(RequestRecord{RequestID: "lost-in-count"}, false, func(*RequestRecord) { panic("count failed") })
	r.submit(RequestRecord{RequestID: "b"}, false, keep)
	r.close()
	if !reflect.DeepEqual(written, []string{"a", "b"}) {
		t.Fatalf("written %v, want [a b]", written)
	}
}

// TestRecorderBatchesFinishedRows: finished rows queued together go out in
// one write, in order, and a row still being counted does not hold up the
// finished rows before it.
func TestRecorderBatchesFinishedRows(t *testing.T) {
	var writes [][]string // the writer's alone until close returns
	wrote := make(chan struct{}, 8)
	r := &recorder{queue: make(chan chan finishedRecord, 8), done: make(chan struct{}), write: func(rows []finishedRecord) {
		var ids []string
		for _, row := range rows {
			ids = append(ids, row.row.RequestID)
		}
		writes = append(writes, ids)
		wrote <- struct{}{}
	}}
	finished := func(id string) chan finishedRecord {
		result := make(chan finishedRecord, 1)
		result <- finishedRecord{row: RequestRecord{RequestID: id}}
		return result
	}
	counting := make(chan finishedRecord, 1)
	for _, result := range []chan finishedRecord{finished("1"), finished("2"), finished("3"), counting, finished("5")} {
		r.queue <- result
	}
	go r.run()
	<-wrote // 1-3 are out while 4 is still being counted
	counting <- finishedRecord{row: RequestRecord{RequestID: "4"}}
	r.close()
	if want := [][]string{{"1", "2", "3"}, {"4", "5"}}; !reflect.DeepEqual(writes, want) {
		t.Fatalf("writes %v, want %v", writes, want)
	}
}
