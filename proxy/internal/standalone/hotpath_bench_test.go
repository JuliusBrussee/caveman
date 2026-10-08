package standalone

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

type benchTransport string

func (t benchTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	_, _ = io.Copy(io.Discard, r.Body)
	return &http.Response{StatusCode: http.StatusOK, Request: r,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(strings.NewReader(string(t)))}, nil
}

func benchMessagesBody(size int) []byte {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	turn := strings.Repeat("the gateway routes the request and keeps the cache prefix stable ", 24)
	var messages []message
	for i := 0; ; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, message{Role: role, Content: turn})
		body, _ := json.Marshal(map[string]any{"model": "claude-sonnet-4-5", "max_tokens": 4096, "messages": messages})
		if len(body) >= size && role == "user" {
			return body
		}
	}
}

// BenchmarkServeRecordMode is the shipped wiring end to end: record mode,
// real SQLite spend store, in-process upstream. c=1 is one caller; c=8 is
// eight callers at once (run with -cpu 8). The store is closed inside the
// timed region, so rows still queued when the last request returns count.
func BenchmarkServeRecordMode(b *testing.B) {
	const response = `{"id":"msg_bench","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1200,"cache_read_input_tokens":48000,"cache_creation_input_tokens":0,"output_tokens":60}}`
	for _, size := range []struct {
		name  string
		bytes int
	}{{"2k", 9_000}, {"50k", 180_000}} {
		body := benchMessagesBody(size.bytes)
		for _, parallel := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/parallel=%v", size.name, parallel), func(b *testing.B) {
				spend, err := store.Open(filepath.Join(b.TempDir(), "caveman.db"), nil)
				if err != nil {
					b.Fatal(err)
				}
				srv := New(config.Config{Mode: "record"}, spend, Options{HTTPClient: &http.Client{Transport: benchTransport(response)}, AsyncRecord: true})
				handler := srv.Handler()
				do := func() {
					req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
					req.Header.Set("x-api-key", "sk-ant-bench")
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)
					if rec.Code != http.StatusOK {
						b.Errorf("status %d: %s", rec.Code, rec.Body.String())
					}
				}
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				b.ResetTimer()
				if parallel {
					b.RunParallel(func(pb *testing.PB) {
						for pb.Next() {
							do()
						}
					})
				} else {
					for i := 0; i < b.N; i++ {
						do()
					}
				}
				_ = srv.Close() // rows still being finished count
				b.StopTimer()
				_ = spend.Close()
			})
		}
	}
}
