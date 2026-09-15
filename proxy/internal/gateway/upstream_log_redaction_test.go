package gateway

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestUpstreamRetryLogRemovesURLCredentials(t *testing.T) {
	var logs bytes.Buffer
	attempts := 0
	// An injected logger need not have the application's redaction hook.
	s := New(Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil)), HTTPClient: &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts++
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		}),
	}})
	_, err := s.doUpstream(context.Background(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, "https://provider.test?key=%41%49%7a%61encoded-credential&%24key=another-credential", strings.NewReader(`{}`))
	})
	if err == nil || attempts != 3 {
		t.Fatalf("retry behavior changed: attempts=%d error=%v", attempts, err)
	}
	if strings.Contains(logs.String(), "credential") || strings.Contains(logs.String(), "%41%49%7a%61") {
		t.Fatalf("retry log leaked credential: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "connection refused") || !strings.Contains(logs.String(), "retrying") {
		t.Fatalf("retry log lost diagnostics: %s", logs.String())
	}
}
