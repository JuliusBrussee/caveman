package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// Dial failures occur before the request reaches the provider and can be retried.
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

	t.Run("succeeds after transient dial failures", func(t *testing.T) {
		attempts := 0
		srv := newServer(func(r *http.Request) (*http.Response, error) {
			attempts++
			if attempts <= 2 {
				_, _ = io.Copy(io.Discard, r.Body)
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
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
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
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

func TestUpstreamAmbiguousFailureNeverReplaysInference(t *testing.T) {
	for _, failure := range []error{
		io.ErrUnexpectedEOF,
		&net.OpError{Op: "write", Net: "tcp", Err: syscall.ECONNRESET},
		context.DeadlineExceeded,
		errors.New("http2: stream error after request upload"),
	} {
		t.Run(failure.Error(), func(t *testing.T) {
			attempts := 0
			s := New(Config{HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				attempts++
				_, _ = io.Copy(io.Discard, r.Body)
				return nil, failure
			})}})
			_, err := s.doUpstream(context.Background(), func() (*http.Request, error) {
				return http.NewRequest(http.MethodPost, "https://provider.test/v1/messages", strings.NewReader(`{"model":"test"}`))
			})
			if err == nil || attempts != 1 {
				t.Fatalf("ambiguous inference replayed: attempts=%d error=%v", attempts, err)
			}
		})
	}
}
