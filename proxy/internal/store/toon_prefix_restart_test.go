package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// A configuration change cannot withdraw a small tool block from memo lookup:
// provider prompt caches need exactly the replacement emitted before restart.
func TestTOONPrefixReplayAfterDisabledRestart(t *testing.T) {
	const original = `{"rows":[{"id":1,"name":"ana","city":"boulder"},{"id":2,"name":"luis","city":"denver"}]}`
	const fresh = `{"rows":[{"id":3,"name":"new","city":"seattle"}]}`
	const replacement = "rows[2]{id,name,city}:\n 1,ana,boulder\n 2,luis,denver\n<<ccr:ccr_toon>>"
	quote := func(value string) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	for _, tc := range []struct {
		name    string
		adapter providers.Adapter
		path    string
		body    func(...string) string
	}{
		{
			name: "anthropic", adapter: anthropic.New("https://upstream.test"), path: "/v1/messages",
			body: func(outputs ...string) string {
				var messages []string
				for _, output := range outputs {
					messages = append(messages, `{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":`+quote(output)+`,"cache_control":{"type":"ephemeral"}}]}`)
				}
				return `{"model":"claude-sonnet-4-6","max_tokens":64,"messages":[` + strings.Join(messages, ",") + `]}`
			},
		},
		{
			name: "openai_chat", adapter: openai.New("https://upstream.test"), path: "/v1/chat/completions",
			body: func(outputs ...string) string {
				var messages []string
				for _, output := range outputs {
					messages = append(messages, `{"role":"tool","tool_call_id":"call_1","content":`+quote(output)+`}`)
				}
				return `{"model":"gpt-5.5","messages":[` + strings.Join(messages, ",") + `]}`
			},
		},
		{
			name: "openai_responses", adapter: openai.New("https://upstream.test"), path: "/v1/responses",
			body: func(outputs ...string) string {
				var items []string
				for _, output := range outputs {
					items = append(items, `{"type":"function_call_output","call_id":"call_1","output":`+quote(output)+`}`)
				}
				return `{"model":"gpt-5.5","input":[` + strings.Join(items, ",") + `]}`
			},
		},
		{
			name: "openai_responses_parts", adapter: openai.New("https://upstream.test"), path: "/v1/responses",
			body: func(outputs ...string) string {
				var items []string
				for _, output := range outputs {
					items = append(items, `{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":`+quote(output)+`}]}`)
				}
				return `{"model":"gpt-5.5","input":[` + strings.Join(items, ",") + `]}`
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAVE_ENGINE_TOON", "best-of")
			path := filepath.Join(t.TempDir(), "prefix.db")
			memo, err := store.Open(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer memo.Close()
			comp := &toonCompressor{}
			transport := &toonTransport{}
			newServer := func(memo *store.Store) *gateway.Server {
				return gateway.New(gateway.Config{
					Adapters: []providers.Adapter{tc.adapter}, Auth: toonAuth{}, Creds: toonCreds{},
					Compressor: comp, PrefixCache: memo, RecoveryViaMCP: true,
					HTTPClient: &http.Client{Transport: transport},
				})
			}
			srv := newServer(memo)
			defer srv.Close()
			send := func(outputs ...string) []byte {
				t.Helper()
				req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body(outputs...)))
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
				}
				return transport.body
			}
			first := append([]byte(nil), send(original)...)
			stored, _, ok := memo.LookupReplacement("unlocked", []byte(original))
			if !ok || string(stored) != replacement || comp.calls != 1 {
				t.Fatalf("initial compression = %q, hit=%v calls=%d", stored, ok, comp.calls)
			}
			if err := srv.Close(); err != nil {
				t.Fatal(err)
			}
			if err := memo.Close(); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CAVE_ENGINE_TOON", "off")
			if segments, _, ok := tc.adapter.ExtractCompressible([]byte(tc.body(original, fresh)), providers.RequestMetadata{Endpoint: tc.path}); ok || len(segments) != 0 {
				t.Errorf("disabled TOON exposed small blocks for new compression: %q", segments)
			}
			memo, err = store.Open(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer memo.Close()
			srv = newServer(memo)
			defer srv.Close()
			if got := send(original); !bytes.Equal(got, first) {
				t.Errorf("same live block changed after disabling TOON and restarting:\nbefore: %s\nafter:  %s", first, got)
			}
			// Move the old result into frozen history. Its replacement must remain,
			// while the new small result must neither compress nor change bytes.
			got := send(original, fresh)
			if bytes.Contains(got, []byte(quote(original))) || !bytes.Contains(got, []byte("rows[2]{id,name,city}")) || !bytes.Contains(got, []byte(quote(fresh))) {
				t.Errorf("frozen replay or fresh passthrough changed: %s", got)
			}
			if comp.calls != 1 {
				t.Errorf("disabled TOON invoked new compression: calls=%d", comp.calls)
			}
			// Re-enabling the transform must not rewrite a block first sent raw.
			t.Setenv("CAVE_ENGINE_TOON", "best-of")
			if again := send(original, fresh); !bytes.Equal(again, got) || comp.calls != 1 {
				t.Errorf("re-enabling TOON changed a prior raw decision: calls=%d body=%s", comp.calls, again)
			}
		})
	}
}

type toonCompressor struct{ calls int }

func (c *toonCompressor) CompressSegment([]byte) ([]byte, int, int) {
	c.calls++
	return []byte("rows[2]{id,name,city}:\n 1,ana,boulder\n 2,luis,denver"), 50, 20
}

func (*toonCompressor) StoreOriginal([]byte) (string, error) { return "ccr_toon", nil }

type toonAuth struct{}

func (toonAuth) Authenticate(context.Context, *http.Request) (gateway.RequestContext, error) {
	return gateway.RequestContext{Label: "local", RuntimeMode: "compress"}, nil
}

type toonCreds struct{}

func (toonCreds) Resolve(string, *http.Request) providers.Credential {
	return providers.Credential{Mode: "ephemeral_header", Key: "sk-byok"}
}

type toonTransport struct{ body []byte }

func (rt *toonTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var err error
	rt.body, err = io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"test","usage":{"input_tokens":100,"output_tokens":1}}`)), Request: req,
	}, nil
}
