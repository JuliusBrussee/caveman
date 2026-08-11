package redact_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

// --- String / scrubbing tests -----------------------------------------------

type stringCase struct {
	name      string
	input     string
	wantClean bool   // true → output must NOT contain the original secret
	keepFrag  string // if set, this fragment must survive in the output
	ruleHit   string // if set, this rule name must appear in fired list
}

var stringCases = []stringCase{
	// cave_live_ project key
	{
		name:      "cave_live_ project key",
		input:     `connection string: cave_live_abcdefghijkl_XYZ0123456789abcdefghijklmno`,
		wantClean: true,
		ruleHit:   "cave-project-key",
	},
	// Bearer token — long enough to trigger
	{
		name:      "bearer token in header value",
		input:     "Authorization: Bearer sk-abcdefghijklmnopqrstuvwxyz1234567890",
		wantClean: true,
		ruleHit:   "bearer-token",
	},
	// Short bearer token (< 20 chars after Bearer) — should NOT be redacted
	{
		name:      "bearer token too short left alone",
		input:     "Authorization: Bearer devtoken",
		wantClean: false, // no scrubbing expected; "devtoken" is only 8 chars
	},
	// AWS access key
	{
		name:      "AWS AKIA access key",
		input:     `aws_access_key_id = AKIAIOSFODNN7EXAMPLE`,
		wantClean: true,
		ruleHit:   "aws-access-key",
	},
	{
		name:      "AWS ASIA temporary key",
		input:     `credentials: ASIAIOSFODNN7EXAMPLE`,
		wantClean: true,
		ruleHit:   "aws-access-key",
	},
	// x-api-key assignment
	{
		name:      "x-api-key JSON value",
		input:     `{"x-api-key": "sk-ant-api03-abcdefghijklmnopqrstuvwxyz123456"}`,
		wantClean: true,
		ruleHit:   "key-assignment",
	},
	// x-goog-api-key
	{
		name:      "x-goog-api-key value",
		input:     `x-goog-api-key: AIzaSyAbcdefghijklmnopqrstuvwxyz`,
		wantClean: true,
		ruleHit:   "key-assignment",
	},
	// OpenAI sk- prefixed key
	{
		name:      "sk- prefixed API key",
		input:     `sk-proj-abcdefghijklmnopqrstuvwxyz1234567890`,
		wantClean: true,
		ruleHit:   "sk-prefixed-key",
	},
	// PEM private key
	{
		name: "PEM RSA private key block",
		input: `-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEA2a2rwplBQLzHPZe5tmmkJGmNmRQAB/OAON8nGJKUHwsKCRvRoLoK
-----END RSA PRIVATE KEY-----`,
		wantClean: true,
		ruleHit:   "pem-private-key",
	},
	// DSN with credentials
	{
		name:      "postgres DSN with password",
		input:     "connecting to postgres://admin:s3cr3tP@ssw0rd@db.example.com:5432/mydb",
		wantClean: true,
		ruleHit:   "dsn-with-credentials",
	},
	{
		name:      "postgres DSN without credentials not redacted",
		input:     "postgres://db.example.com:5432/mydb",
		wantClean: false, // no userinfo → not a credential
		keepFrag:  "postgres://db.example.com",
	},
	// Benign text must survive untouched
	{
		name:      "benign log line unchanged",
		input:     "user 1234 logged in from 198.51.100.1",
		wantClean: false,
		keepFrag:  "user 1234 logged in from 198.51.100.1",
	},
	{
		name:      "hostname with dots not redacted",
		input:     "upstream error from api.openai.com:443",
		wantClean: false,
		keepFrag:  "api.openai.com",
	},
	{
		name:      "empty string",
		input:     "",
		wantClean: false,
	},
}

func TestString(t *testing.T) {
	for _, tc := range stringCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			out, fired := redact.String(tc.input)

			if tc.wantClean {
				// The redacted output must not contain the original input verbatim
				// (i.e. something was changed).
				if out == tc.input {
					t.Fatalf("expected input to be redacted, got unchanged output:\n%s", out)
				}
				// Output must not contain known plaintext markers.
				if strings.Contains(out, "PRIVATE KEY") || strings.Contains(out, "AKIA") {
					t.Fatalf("redacted output still contains secret material:\n%s", out)
				}
			}

			if tc.keepFrag != "" && !strings.Contains(out, tc.keepFrag) {
				t.Fatalf("expected fragment %q to survive in output:\n%s", tc.keepFrag, out)
			}

			if tc.ruleHit != "" {
				found := false
				for _, r := range fired {
					if r == tc.ruleHit {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected rule %q to fire, got fired=%v", tc.ruleHit, fired)
				}
			}
		})
	}
}

// --- IsSensitiveHeader tests -------------------------------------------------

type headerCase struct {
	name      string
	headerKey string
	want      bool
}

var headerCases = []headerCase{
	{name: "authorization", headerKey: "Authorization", want: true},
	{name: "authorization lowercase", headerKey: "authorization", want: true},
	{name: "cookie", headerKey: "Cookie", want: true},
	{name: "set-cookie", headerKey: "Set-Cookie", want: true},
	{name: "proxy-authorization", headerKey: "Proxy-Authorization", want: true},
	{name: "x-api-key", headerKey: "X-Api-Key", want: true},
	{name: "api-key", headerKey: "Api-Key", want: true},
	{name: "x-goog-api-key", headerKey: "X-Goog-Api-Key", want: true},
	{name: "x-amz-security-token", headerKey: "X-Amz-Security-Token", want: true},
	{name: "x-custom-secret", headerKey: "X-Custom-Secret", want: true},
	{name: "my-api-key-header", headerKey: "My-Api-Key-Header", want: true},
	// Benign headers
	{name: "content-type benign", headerKey: "Content-Type", want: false},
	{name: "accept benign", headerKey: "Accept", want: false},
	{name: "user-agent benign", headerKey: "User-Agent", want: false},
	{name: "x-request-id benign", headerKey: "X-Request-Id", want: false},
	{name: "x-cave-request-id benign", headerKey: "X-Cave-Request-Id", want: false},
	{name: "anthropic-version benign", headerKey: "Anthropic-Version", want: false},
}

func TestIsSensitiveHeader(t *testing.T) {
	for _, tc := range headerCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := redact.IsSensitiveHeader(tc.headerKey)
			if got != tc.want {
				t.Fatalf("IsSensitiveHeader(%q) = %v, want %v", tc.headerKey, got, tc.want)
			}
		})
	}
}

// --- ScrubHeaders tests ------------------------------------------------------

func TestScrubHeaders(t *testing.T) {
	h := http.Header{
		"Authorization": []string{"Bearer sk-super-secret-api-key-12345678"},
		"X-Api-Key":     []string{"my-goog-key-abcdefgh"},
		"Content-Type":  []string{"application/json"},
		"Accept":        []string{"*/*"},
	}

	scrubbed := redact.ScrubHeaders(h)

	// Sensitive headers must be replaced.
	if v := scrubbed.Get("Authorization"); v != "[REDACTED]" {
		t.Fatalf("Authorization not scrubbed, got %q", v)
	}
	if v := scrubbed.Get("X-Api-Key"); v != "[REDACTED]" {
		t.Fatalf("X-Api-Key not scrubbed, got %q", v)
	}

	// Benign headers must survive.
	if v := scrubbed.Get("Content-Type"); v != "application/json" {
		t.Fatalf("Content-Type changed, got %q", v)
	}
	if v := scrubbed.Get("Accept"); v != "*/*" {
		t.Fatalf("Accept changed, got %q", v)
	}

	// Original must not be mutated.
	if h.Get("Authorization") == "[REDACTED]" {
		t.Fatal("ScrubHeaders mutated the original header map")
	}
}

func TestScrubHeaders_NilSafe(t *testing.T) {
	out := redact.ScrubHeaders(nil)
	if out == nil {
		t.Fatal("ScrubHeaders(nil) returned nil, expected empty map")
	}
}

// --- Error tests -------------------------------------------------------------

func TestError_NilReturnsEmpty(t *testing.T) {
	if s := redact.Error(nil); s != "" {
		t.Fatalf("expected empty string for nil error, got %q", s)
	}
}

func TestError_ScrubsSecretInMessage(t *testing.T) {
	err := errors.New("upstream auth failed: cave_live_abcdefghijkl_SECRETSECRET1234567890")
	s := redact.Error(err)
	if strings.Contains(s, "cave_live_") {
		t.Fatalf("Error() did not scrub cave_live_ key: %q", s)
	}
	if !strings.Contains(s, "upstream auth failed") {
		t.Fatalf("Error() removed benign message context: %q", s)
	}
}

func TestError_BenignMessageUnchanged(t *testing.T) {
	msg := "connection refused to api.openai.com:443"
	err := errors.New(msg)
	s := redact.Error(err)
	if s != msg {
		t.Fatalf("Error() changed benign message: got %q, want %q", s, msg)
	}
}

func TestSlogReplaceAttrScrubsStringAndErrorValues(t *testing.T) {
	const secret = "cave_live_abcdefghijkl_SECRETSECRET1234567890"
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{ReplaceAttr: redact.SlogReplaceAttr}))
	logger.Error("request failed", "dsn", "postgres://admin:supersecretpassword@db.example/cave", "error", errors.New("upstream rejected "+secret))

	logged := out.String()
	if strings.Contains(logged, secret) || strings.Contains(logged, "supersecretpassword") {
		t.Fatalf("slog hook leaked secret: %s", logged)
	}
	if !strings.Contains(logged, "request failed") || !strings.Contains(logged, "upstream rejected") {
		t.Fatalf("slog hook removed useful context: %s", logged)
	}
}
