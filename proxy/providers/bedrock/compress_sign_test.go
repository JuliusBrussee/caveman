package bedrock

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/awssig"
)

// S11 regression. Two invariants the managed gateway relies on:
//
//  1. The S4 compressor must SKIP every Bedrock vendor (Anthropic or not), so
//     compress mode never reshapes a Bedrock body. Bedrock's separate, opt-in
//     provider-native cache marker does not register schema-aware extraction.
//  2. SigV4 is valid over the exact wire bytes because the adapter hashes the
//     replayable request body after all transforms. The credential parse and a
//     missing exact payload hash both fail closed (no unsigned passthrough).
//
// These tests pin both so a future change that teaches the S4 compressor to
// reshape Bedrock bodies, signs the wrong payload, or falls back to unsigned
// fails loudly.

// bedrockVendors spans the model-id allowlist (routing.go defaultModelPrefixes):
// Anthropic plus several non-Anthropic vendors. Compression must be skipped for all.
var bedrockVendors = []string{
	"anthropic.claude-3-5-sonnet-20241022-v2:0",
	"amazon.titan-text-express-v1",
	"amazon.nova-pro-v1:0",
	"meta.llama3-70b-instruct-v1:0",
	"mistral.mistral-large-2407-v1:0",
	"cohere.command-r-plus-v1:0",
}

// TestExtractCompressible_OptsOutForEveryBedrockVendor asserts no Bedrock vendor's body
// is ever reshaped by compress mode — including a realistic Anthropic-on-Bedrock body,
// the one case a naive compressor might try to compress.
func TestExtractCompressible_OptsOutForEveryBedrockVendor(t *testing.T) {
	a := newAdapter(t)
	body := []byte(`{"anthropic_version":"bedrock-2023-05-31","messages":[{"role":"user","content":"` +
		strings.Repeat("compress me ", 200) + `"}]}`)
	for _, model := range bedrockVendors {
		meta := providers.RequestMetadata{Provider: "bedrock", Model: model, Endpoint: "invoke"}
		segments, reassemble, ok := a.ExtractCompressible(body, meta)
		if ok || segments != nil || reassemble != nil {
			t.Errorf("model %s: ExtractCompressible ok=%v segments=%d reassemble!=nil:%v — Bedrock must never reshape a body (byte-safe passthrough)",
				model, ok, len(segments), reassemble != nil)
		}
	}
}

// TestNonAnthropicVendor_ForwardedSignedUncompressed is the direct S11 acceptance: a
// non-anthropic.* Bedrock request is allowed (forwarded), is NOT compressed, and is
// signed with SigV4 over its exact payload — never an unsigned passthrough.
func TestNonAnthropicVendor_ForwardedSignedUncompressed(t *testing.T) {
	a := newAdapter(t)
	const model = "meta.llama3-70b-instruct-v1:0" // non-Anthropic vendor
	body := `{"prompt":"hello","max_gen_len":128}`

	req, _ := http.NewRequest(http.MethodPost, invokePath(model, "invoke"), strings.NewReader(body))
	req.Header.Set("x-cave-aws-region", "us-east-1")
	req.Header.Set("content-type", "application/json")

	// (a) route resolves — a non-Anthropic vendor is on the allowlist, not rejected.
	if _, err := a.ResolveUpstreamURL(context.Background(), req, providers.RouteContext{}); err != nil {
		t.Fatalf("non-anthropic vendor must be forwarded (allowed), got: %v", err)
	}

	// (b) not compressed.
	meta := providers.RequestMetadata{Provider: "bedrock", Model: model, Endpoint: "invoke"}
	if _, _, ok := a.ExtractCompressible([]byte(body), meta); ok {
		t.Error("non-anthropic vendor body must not be compressed")
	}

	// (c) signed with SigV4 over the exact wire bytes, never an unsigned
	// passthrough.
	upstream, _ := a.ResolveUpstreamURL(context.Background(), req, providers.RouteContext{})
	out, err := a.SanitizeAndMapHeaders(context.Background(), req, providers.Credential{Key: "AKIAIOSFODNN7EXAMPLE:" + testSecret}, upstream)
	if err != nil {
		t.Fatalf("signing failed: %v", err)
	}
	auth := out.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || !strings.Contains(auth, "Signature=") {
		t.Errorf("missing SigV4 Authorization: %q", auth)
	}
	if out.Get("X-Amz-Content-Sha256") != awssig.HashPayload([]byte(body)) {
		t.Errorf("content-sha256 = %q, want exact wire-body SHA-256", out.Get("X-Amz-Content-Sha256"))
	}
}

// TestSignedHostMatchesForwardURL pins the fix for the managed-path signing bug:
// SigV4 always signs Host, so the signed Host must equal the URL the proxy forwards
// to. When a project configures a per-connection Bedrock base URL (a different region
// or endpoint than the adapter's construction-time default), signing must use THAT
// resolved upstream — not re-resolve to the adapter default — or AWS rejects the
// request as SignatureDoesNotMatch. The proxy passes the already-resolved upstream URL;
// this asserts the signature follows it (Host + region scope).
func TestSignedHostMatchesForwardURL(t *testing.T) {
	a := newAdapter(t) // adapter default BaseURL is us-east-1 (stubBase)
	req, _ := http.NewRequest(http.MethodPost, invokePath(claudeModel, "invoke"), strings.NewReader(`{"messages":[]}`))
	// No x-cave-aws-region header: the region must be inferred from the forward host.

	// The proxy resolved a per-project endpoint in a DIFFERENT region than the default.
	forward, _ := url.Parse("https://bedrock-runtime.eu-west-1.amazonaws.com/model/" + claudeModel + "/invoke")
	out, err := a.SanitizeAndMapHeaders(context.Background(), req, providers.Credential{Key: "AKIAIOSFODNN7EXAMPLE:" + testSecret}, forward)
	if err != nil {
		t.Fatalf("signing failed: %v", err)
	}
	if got := out.Get("Host"); got != "bedrock-runtime.eu-west-1.amazonaws.com" {
		t.Errorf("signed Host = %q, want the forward host bedrock-runtime.eu-west-1.amazonaws.com (not the adapter default)", got)
	}
	if auth := out.Get("Authorization"); !strings.Contains(auth, "/eu-west-1/bedrock/aws4_request") {
		t.Errorf("SigV4 scope = %q, want region eu-west-1 inferred from the forward host (host/region must agree)", auth)
	}
}
