package gateway

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// A transport failure before any response byte (connection reset during a large
// upload) must be replayed by the proxy, not surfaced as a terminal 502 that
// kills the agent. Zero bytes have reached the client at that point, so the
// retry is invisible on success.
func TestUpstreamTransportErrorRetries(t *testing.T) {
	const body = `{"model":"claude-sonnet-5","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}`
	const response = `{"id":"msg","type":"message","model":"claude-sonnet-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`

	newServer := func(transport roundTripFunc) *Server {
		return New(Config{
			Adapters:   []providers.Adapter{anthropic.New("https://upstream.test")},
			Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
			Creds:      passthroughTestCreds{},
			Sink:       &captureSink{},
			HTTPClient: &http.Client{Transport: transport},
		})
	}

	t.Run("succeeds after transient resets", func(t *testing.T) {
		attempts := 0
		srv := newServer(func(r *http.Request) (*http.Response, error) {
			attempts++
			if attempts <= 2 {
				_, _ = io.Copy(io.Discard, r.Body)
				return nil, errors.New("write tcp: connection reset by peer")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"content-type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(response)),
				Request:    r,
			}, nil
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", "sk-test")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
	})

	t.Run("exhausted retries still 502", func(t *testing.T) {
		attempts := 0
		srv := newServer(func(r *http.Request) (*http.Response, error) {
			attempts++
			return nil, errors.New("write tcp: connection reset by peer")
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", "sk-test")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
	})
}
