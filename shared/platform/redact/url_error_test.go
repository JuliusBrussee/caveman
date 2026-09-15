package redact_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

func TestErrorRemovesTransportURLsWithoutChangingCause(t *testing.T) {
	cause := errors.New("connection refused")
	for _, endpoint := range []string{
		"https://provider.test/v1/models?key=unprefixed-account-credential",
		"https://provider.test/v1/models?%24key=%41%49%7a%61encodedcredential",
		"https://user:private-password@provider.test/v1/models",
		"https://provider.test/private-path-token?quote=\"quoted-secret\"",
	} {
		uerr := &url.Error{Op: "Post", URL: endpoint, Err: cause}
		err := fmt.Errorf("provider request failed: %w", uerr)
		before := err.Error()
		got := redact.Error(err)
		for _, secret := range []string{endpoint, "credential", "private-password", "quoted-secret", "private-path-token"} {
			if strings.Contains(got, secret) {
				t.Errorf("URL credential leaked: %s", got)
			}
		}
		if !strings.Contains(got, "provider request failed") || !strings.Contains(got, cause.Error()) {
			t.Errorf("lost error context: %s", got)
		}
		if err.Error() != before || uerr.URL != endpoint || !errors.Is(err, cause) {
			t.Fatal("redaction changed the original error")
		}
	}
}

func TestSlogRemovesJoinedAndNestedTransportURLs(t *testing.T) {
	err := errors.Join(
		&url.Error{Op: "Post", URL: "https://first.test?key=first-secret", Err: errors.New("dial refused")},
		fmt.Errorf("fallback: %w", &url.Error{Op: "Post", URL: "https://second.test?key=second-secret", Err: &url.Error{
			Op: "Connect", URL: "https://user:proxy-secret@proxy.test", Err: errors.New("proxy refused"),
		}}),
	)
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{ReplaceAttr: redact.SlogReplaceAttr}))
	logger.Error("upstream failed", "error", err)
	for _, secret := range []string{"first-secret", "second-secret", "proxy-secret"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("slog leaked URL credential: %s", out.String())
		}
	}
	if !strings.Contains(out.String(), "dial refused") || !strings.Contains(out.String(), "proxy refused") {
		t.Fatalf("lost transport diagnostics: %s", out.String())
	}
}

func TestErrorRemovesURLsWithSharedPrefixes(t *testing.T) {
	err := errors.Join(
		&url.Error{Op: "Post", URL: "https://provider.test/base", Err: errors.New("first failed")},
		&url.Error{Op: "Post", URL: "https://provider.test/base?opaque=SUPERSECRET", Err: errors.New("second failed")},
	)
	if got := redact.Error(err); strings.Contains(got, "SUPERSECRET") {
		t.Fatalf("shared URL prefix left a credential suffix: %s", got)
	}
}

// A malformed base URL reaches Error as *url.Error{Op:"parse", URL:"<input>"}.
// Replacing a one-character URL everywhere shredded the message AND split
// bearer tokens into fragments too short for String's patterns, so the token
// survived in clear text. Short, non-endpoint values are left alone.
func TestErrorLeavesShortNonEndpointURLsAlone(t *testing.T) {
	const token = "Bearer QQQzRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR-TAIL-SECRET"
	for _, raw := range []string{"z", ":", "a", "http://"} {
		err := fmt.Errorf("bad upstream: %w | Authorization: %s", &url.Error{Op: "parse", URL: raw, Err: errors.New("invalid")}, token)
		got := redact.Error(err)
		if strings.Contains(got, "SECRET") || strings.Contains(got, "[REDACTED:url]") {
			t.Fatalf("url %q: %q", raw, got)
		}
	}
	endpoint := &url.Error{Op: "Post", URL: "https://provider.test/v1?key=SUPERSECRET", Err: errors.New("refused")}
	if got := redact.Error(endpoint); strings.Contains(got, "SUPERSECRET") || !strings.Contains(got, "[REDACTED:url]") {
		t.Fatalf("real endpoint not redacted: %q", got)
	}
}
