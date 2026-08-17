package kms_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/kms"
)

const testKeyID = "6170692e-7363-416c-a577-61792e636f6d"
const otherTestKeyID = "11111111-2222-4333-8444-555555555555"

func newClient(t *testing.T, server *httptest.Server, allowed ...string) *kms.Client {
	t.Helper()
	cfg := kms.Config{
		Provider: "scaleway", Region: "fr-par", KeyID: testKeyID,
		AuthToken: strings.Repeat("t", 32), AllowedDecryptKeyIDs: allowed,
	}
	if server != nil {
		cfg.APIBaseURL = server.URL
		cfg.HTTPClient = server.Client()
	}
	client, err := kms.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func envelope(t *testing.T, value kms.Envelope) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte("cave-kms-v1:"), raw...)
}

func TestEncryptDecryptScalewayEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-Token") != strings.Repeat("t", 32) {
			t.Error("missing KMS auth token")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/encrypt"):
			if got, _ := base64.StdEncoding.DecodeString(body["plaintext"]); string(got) != "secret" {
				t.Errorf("plaintext = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"key_id": testKeyID, "ciphertext": "opaque-kms-value"})
		case strings.HasSuffix(r.URL.Path, "/decrypt"):
			_ = json.NewEncoder(w).Encode(map[string]string{"key_id": testKeyID, "plaintext": base64.StdEncoding.EncodeToString([]byte("secret"))})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := kms.New(kms.Config{
		Provider: "scaleway", Region: "fr-par", KeyID: testKeyID,
		AuthToken: strings.Repeat("t", 32), APIBaseURL: server.URL, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := client.Encrypt(context.Background(), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if !kms.IsEnvelope(wrapper) || strings.Contains(string(wrapper), "secret") {
		t.Fatalf("unsafe envelope: %q", wrapper)
	}
	plain, err := client.Decrypt(context.Background(), wrapper)
	if err != nil || string(plain) != "secret" {
		t.Fatalf("Decrypt() = %q, %v", plain, err)
	}
}

func TestProbePerformsLiveEncryptDecryptRoundTrip(t *testing.T) {
	var plaintext []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/encrypt"):
			plaintext, _ = base64.StdEncoding.DecodeString(body["plaintext"])
			_ = json.NewEncoder(w).Encode(map[string]string{"key_id": testKeyID, "ciphertext": "probe-ciphertext"})
		case strings.HasSuffix(r.URL.Path, "/decrypt"):
			_ = json.NewEncoder(w).Encode(map[string]string{"key_id": testKeyID, "plaintext": base64.StdEncoding.EncodeToString(plaintext)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := kms.New(kms.Config{
		Provider: "scaleway", Region: "fr-par", KeyID: testKeyID,
		AuthToken: strings.Repeat("t", 32), APIBaseURL: server.URL, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(plaintext) != 32 {
		t.Fatalf("probe plaintext length = %d", len(plaintext))
	}
}

func TestEncryptRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/internal", http.StatusFound)
	}))
	defer server.Close()
	client, err := kms.New(kms.Config{
		Provider: "scaleway", Region: "fr-par", KeyID: testKeyID,
		AuthToken: strings.Repeat("t", 32), APIBaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Encrypt(context.Background(), []byte("secret")); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("Encrypt() error = %v", err)
	}
}

func TestValidateProductionFailsClosed(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("CAVE_KMS_PROVIDER", "")
	if err := kms.ValidateProduction(); err == nil {
		t.Fatal("production accepted missing KMS provider")
	}
}

func TestDecryptRejectsOversizedEnvelopeBeforeNetwork(t *testing.T) {
	client, err := kms.New(kms.Config{
		Provider: "scaleway", Region: "fr-par", KeyID: testKeyID,
		AuthToken: strings.Repeat("t", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	oversized := append([]byte("cave-kms-v1:"), []byte(strings.Repeat("x", 300<<10))...)
	if _, err := client.Decrypt(context.Background(), oversized); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("Decrypt() error = %v", err)
	}
}

func TestDecryptRejectsEnvelopeKeyOutsideExplicitAllowlist(t *testing.T) {
	client, err := kms.New(kms.Config{
		Provider: "scaleway", Region: "fr-par", KeyID: testKeyID,
		AuthToken: strings.Repeat("t", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(kms.Envelope{
		Provider: "scaleway", Region: "fr-par", KeyID: otherTestKeyID, Ciphertext: "opaque",
	})
	envelope := append([]byte("cave-kms-v1:"), body...)
	if _, err := client.Decrypt(context.Background(), envelope); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("Decrypt() error = %v", err)
	}
}

func TestValidatePayloadProductionRequiresDedicatedKey(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("CAVE_KMS_PROVIDER", "scaleway")
	t.Setenv("CAVE_KMS_REGION", "fr-par")
	t.Setenv("CAVE_KMS_AUTH_TOKEN", strings.Repeat("t", 32))
	t.Setenv("KMS_SECRETS_KEY_ARN", testKeyID)
	t.Setenv("KMS_PAYLOADS_KEY_ARN", "")
	if err := kms.ValidatePayloadProduction(); err == nil {
		t.Fatal("production accepted missing payload KMS key")
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	valid := kms.Config{
		Provider: "scaleway", Region: "fr-par", KeyID: testKeyID,
		AuthToken: strings.Repeat("t", 32),
	}
	tests := []struct {
		name   string
		mutate func(*kms.Config)
	}{
		{name: "unsupported provider", mutate: func(c *kms.Config) { c.Provider = "aws" }},
		{name: "invalid region", mutate: func(c *kms.Config) { c.Region = "local" }},
		{name: "invalid key", mutate: func(c *kms.Config) { c.KeyID = "key" }},
		{name: "invalid allowed key", mutate: func(c *kms.Config) { c.AllowedDecryptKeyIDs = []string{"bad"} }},
		{name: "short auth token", mutate: func(c *kms.Config) { c.AuthToken = "short" }},
		{name: "relative base URL", mutate: func(c *kms.Config) { c.APIBaseURL = "/relative" }},
		{name: "base URL userinfo", mutate: func(c *kms.Config) { c.APIBaseURL = "https://user@example.com" }},
		{name: "base URL query", mutate: func(c *kms.Config) { c.APIBaseURL = "https://example.com?x=1" }},
		{name: "base URL fragment", mutate: func(c *kms.Config) { c.APIBaseURL = "https://example.com#x" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			if _, err := kms.New(cfg); err == nil {
				t.Fatal("invalid KMS configuration accepted")
			}
		})
	}
}

func TestEnvironmentConfigurationAndWrappers(t *testing.T) {
	t.Setenv("CAVE_ENV", "dev")
	t.Setenv("CAVE_KMS_PROVIDER", "scaleway")
	t.Setenv("CAVE_KMS_REGION", "")
	t.Setenv("SCW_DEFAULT_REGION", "fr-par")
	t.Setenv("CAVE_KMS_AUTH_TOKEN", "")
	t.Setenv("SCW_SECRET_KEY", strings.Repeat("s", 32))
	t.Setenv("KMS_SECRETS_KEY_ARN", testKeyID)
	t.Setenv("KMS_PAYLOADS_KEY_ARN", otherTestKeyID)
	if _, err := kms.FromEnvironment(); err != nil {
		t.Fatalf("FromEnvironment fallback config: %v", err)
	}
	if _, err := kms.FromPayloadEnvironment(); err != nil {
		t.Fatalf("FromPayloadEnvironment rotation config: %v", err)
	}
	if err := kms.ValidateProduction(); err != nil {
		t.Fatalf("non-production validation = %v", err)
	}
	if err := kms.ValidatePayloadProduction(); err != nil {
		t.Fatalf("non-production payload validation = %v", err)
	}
	if err := kms.ProbeProduction(context.Background()); err != nil {
		t.Fatalf("non-production probe = %v", err)
	}
	if err := kms.ProbePayloadProduction(context.Background()); err != nil {
		t.Fatalf("non-production payload probe = %v", err)
	}

	t.Setenv("CAVE_KMS_PROVIDER", "")
	if _, err := kms.Encrypt(context.Background(), []byte("x")); err == nil {
		t.Fatal("Encrypt wrapper accepted invalid environment")
	}
	if _, err := kms.EncryptPayload(context.Background(), []byte("x")); err == nil {
		t.Fatal("EncryptPayload wrapper accepted invalid environment")
	}
	if _, err := kms.Decrypt(context.Background(), []byte("x")); err == nil {
		t.Fatal("Decrypt wrapper accepted invalid environment")
	}
	if _, err := kms.DecryptPayload(context.Background(), []byte("x")); err == nil {
		t.Fatal("DecryptPayload wrapper accepted invalid environment")
	}
}

func TestEncryptInputAndResponseValidation(t *testing.T) {
	client := newClient(t, nil)
	if _, err := client.Encrypt(context.Background(), nil); err == nil {
		t.Fatal("empty plaintext accepted")
	}
	if _, err := client.Encrypt(context.Background(), make([]byte, 65536)); err == nil {
		t.Fatal("oversized plaintext accepted")
	}

	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http error", status: http.StatusForbidden, body: `{}`},
		{name: "malformed response", status: http.StatusOK, body: `{`},
		{name: "wrong response key", status: http.StatusOK, body: `{"key_id":"` + otherTestKeyID + `","ciphertext":"value"}`},
		{name: "empty ciphertext", status: http.StatusOK, body: `{"key_id":"` + testKeyID + `","ciphertext":" "}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			if _, err := newClient(t, server).Encrypt(context.Background(), []byte("secret")); err == nil {
				t.Fatal("invalid encrypt response accepted")
			}
		})
	}
}

func TestDecryptEnvelopeValidation(t *testing.T) {
	client := newClient(t, nil)
	if kms.IsEnvelope([]byte("plain")) || !kms.IsEnvelope([]byte("cave-kms-v1:{}")) {
		t.Fatal("IsEnvelope classification drifted")
	}
	tests := []struct {
		name string
		blob []byte
	}{
		{name: "unknown format", blob: []byte("plain")},
		{name: "malformed json", blob: []byte("cave-kms-v1:{")},
		{name: "wrong provider", blob: envelope(t, kms.Envelope{Provider: "aws", Region: "fr-par", KeyID: testKeyID, Ciphertext: "x"})},
		{name: "invalid region", blob: envelope(t, kms.Envelope{Provider: "scaleway", Region: "local", KeyID: testKeyID, Ciphertext: "x"})},
		{name: "invalid key", blob: envelope(t, kms.Envelope{Provider: "scaleway", Region: "fr-par", KeyID: "bad", Ciphertext: "x"})},
		{name: "unapproved region", blob: envelope(t, kms.Envelope{Provider: "scaleway", Region: "nl-ams", KeyID: testKeyID, Ciphertext: "x"})},
		{name: "empty ciphertext", blob: envelope(t, kms.Envelope{Provider: "scaleway", Region: "fr-par", KeyID: testKeyID, Ciphertext: " "})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := client.Decrypt(context.Background(), tt.blob); err == nil {
				t.Fatal("invalid envelope accepted")
			}
		})
	}
}

func TestDecryptResponseValidationAndRotatedKey(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http error", status: http.StatusForbidden, body: `{}`},
		{name: "malformed response", status: http.StatusOK, body: `{`},
		{name: "wrong key", status: http.StatusOK, body: `{"key_id":"` + testKeyID + `","plaintext":"eA=="}`},
		{name: "empty plaintext", status: http.StatusOK, body: `{"key_id":"` + otherTestKeyID + `","plaintext":""}`},
		{name: "invalid base64", status: http.StatusOK, body: `{"key_id":"` + otherTestKeyID + `","plaintext":"!"}`},
		{name: "zero decoded bytes", status: http.StatusOK, body: `{"key_id":"` + otherTestKeyID + `","plaintext":"=="}`},
		{name: "oversized plaintext", status: http.StatusOK, body: `{"key_id":"` + otherTestKeyID + `","plaintext":"` + base64.StdEncoding.EncodeToString(make([]byte, 65536)) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			client := newClient(t, server, otherTestKeyID)
			blob := envelope(t, kms.Envelope{
				Provider: "scaleway", Region: "fr-par", KeyID: otherTestKeyID, Ciphertext: "cipher",
			})
			if _, err := client.Decrypt(context.Background(), blob); err == nil {
				t.Fatal("invalid decrypt response accepted")
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"key_id": otherTestKeyID, "plaintext": base64.StdEncoding.EncodeToString([]byte("rotated")),
		})
	}))
	defer server.Close()
	got, err := newClient(t, server, otherTestKeyID).Decrypt(context.Background(), envelope(t, kms.Envelope{
		Provider: "scaleway", Region: "fr-par", KeyID: otherTestKeyID, Ciphertext: "cipher",
	}))
	if err != nil || string(got) != "rotated" {
		t.Fatalf("rotated-key Decrypt() = %q, %v", got, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingBody) Close() error             { return nil }

func TestKMSCallTransportReadAndResponseLimits(t *testing.T) {
	tests := []struct {
		name      string
		transport roundTripFunc
	}{
		{
			name: "transport",
			transport: func(*http.Request) (*http.Response, error) {
				return nil, errors.New("network failed")
			},
		},
		{
			name: "read",
			transport: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: failingBody{}, Header: make(http.Header)}, nil
			},
		},
		{
			name: "oversized response",
			transport: func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", (512<<10)+1))),
					Header:     make(http.Header),
				}, nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := kms.New(kms.Config{
				Provider: "scaleway", Region: "fr-par", KeyID: testKeyID,
				AuthToken:  strings.Repeat("t", 32),
				HTTPClient: &http.Client{Transport: tt.transport},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Encrypt(context.Background(), []byte("secret")); err == nil {
				t.Fatal("call failure ignored")
			}
		})
	}
}

func TestProbeFailureStagesAndMismatch(t *testing.T) {
	tests := []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
	}{
		{
			name: "encrypt",
			fn: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "failed", http.StatusForbidden)
			},
		},
		{
			name: "decrypt",
			fn: func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/encrypt") {
					_ = json.NewEncoder(w).Encode(map[string]string{"key_id": testKeyID, "ciphertext": "x"})
					return
				}
				http.Error(w, "failed", http.StatusForbidden)
			},
		},
		{
			name: "mismatch",
			fn: func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/encrypt") {
					_ = json.NewEncoder(w).Encode(map[string]string{"key_id": testKeyID, "ciphertext": "x"})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{
					"key_id": testKeyID, "plaintext": base64.StdEncoding.EncodeToString([]byte("different")),
				})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.fn))
			defer server.Close()
			if err := newClient(t, server).Probe(context.Background()); err == nil {
				t.Fatal("probe failure ignored")
			}
		})
	}
}
