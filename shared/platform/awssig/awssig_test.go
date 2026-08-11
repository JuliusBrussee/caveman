package awssig_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/awssig"
)

// This uses the AWS documentation example credentials, time and request URL
// (https://docs.aws.amazon.com/IAM/latest/UserGuide/create-signed-request.html),
// but signs the header set this signer actually produces
// (host;x-amz-content-sha256;x-amz-date) rather than the abbreviated
// content-type;host;x-amz-date set in the doc walkthrough — bedrock-runtime and
// S3 both sign x-amz-content-sha256. The expected signature was computed by an
// INDEPENDENT stdlib reference implementation of SigV4 over the identical
// canonical form, so a match proves the canonical request, string-to-sign,
// signing-key derivation and final HMAC are all correct.
func TestSign_MatchesReferenceSignature(t *testing.T) {
	creds := awssig.Credentials{
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}
	req, err := http.NewRequest(http.MethodGet,
		"https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	signer := awssig.Signer{Region: "us-east-1", Service: "iam"}
	signingTime := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	if err := signer.Sign(req, creds, awssig.HashPayload(nil), signingTime); err != nil {
		t.Fatalf("sign: %v", err)
	}

	auth := req.Header.Get("Authorization")
	const wantSig = "732998440eb24c9e1d86f1c78922254b7583f3a67759c5686691725187bb95b6"
	if !strings.Contains(auth, "Signature="+wantSig) {
		t.Errorf("Authorization signature mismatch.\n got: %s\nwant signature: %s", auth, wantSig)
	}
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20150830/us-east-1/iam/aws4_request") {
		t.Errorf("Authorization credential scope wrong: %s", auth)
	}
	if want := "host;x-amz-content-sha256;x-amz-date"; !strings.Contains(auth, "SignedHeaders="+want) {
		t.Errorf("SignedHeaders mismatch in %s, want %s", auth, want)
	}
	if req.Header.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Errorf("X-Amz-Date = %q, want 20150830T123600Z", req.Header.Get("X-Amz-Date"))
	}
}

// HONESTY/SECURITY: the secret access key must never appear in any header the
// signer sets on the request. Only the (non-reversible) signature digest and the
// access key ID may be observable.
func TestSign_SecretNeverAppearsInHeaders(t *testing.T) {
	const secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	creds := awssig.Credentials{
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: secret,
		SessionToken:    "FwoSESSIONTOKENvalue==",
	}
	req, _ := http.NewRequest(http.MethodPost,
		"https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude/invoke",
		strings.NewReader(`{"prompt":"hi"}`))
	req.Header.Set("Content-Type", "application/json")

	signer := awssig.Signer{Region: "us-east-1", Service: "bedrock"}
	if err := signer.Sign(req, creds, awssig.HashPayload([]byte(`{"prompt":"hi"}`)), time.Now()); err != nil {
		t.Fatalf("sign: %v", err)
	}
	for name, values := range req.Header {
		for _, v := range values {
			if strings.Contains(v, secret) {
				t.Fatalf("secret access key leaked into header %s = %q", name, v)
			}
		}
	}
	// The session token, by contrast, is a header AWS requires on the wire.
	if req.Header.Get("X-Amz-Security-Token") != creds.SessionToken {
		t.Errorf("session token not set: %q", req.Header.Get("X-Amz-Security-Token"))
	}
	if !strings.Contains(req.Header.Get("Authorization"), "Signature=") {
		t.Errorf("no signature produced: %q", req.Header.Get("Authorization"))
	}
}

func TestSign_RejectsMissingCredentials(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	signer := awssig.Signer{Region: "us-east-1", Service: "bedrock"}
	if err := signer.Sign(req, awssig.Credentials{AccessKeyID: "only-id"}, awssig.HashPayload(nil), time.Now()); err == nil {
		t.Error("signing without a secret access key should fail")
	}
	if err := signer.Sign(req, awssig.Credentials{}, awssig.HashPayload(nil), time.Now()); err == nil {
		t.Error("signing without credentials should fail")
	}
	if err := (awssig.Signer{Service: "bedrock"}).Sign(req, awssig.Credentials{AccessKeyID: "a", SecretAccessKey: "b"}, awssig.HashPayload(nil), time.Now()); err == nil {
		t.Error("signing without a region should fail")
	}
}

func TestSign_CanonicalizesQueryDeterministically(t *testing.T) {
	creds := awssig.Credentials{AccessKeyID: "AKID", SecretAccessKey: "secret"}
	signer := awssig.Signer{Region: "us-west-2", Service: "bedrock"}
	at := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)

	mk := func(rawurl string) string {
		req, _ := http.NewRequest(http.MethodGet, rawurl, nil)
		if err := signer.Sign(req, creds, awssig.HashPayload(nil), at); err != nil {
			t.Fatalf("sign: %v", err)
		}
		return req.Header.Get("Authorization")
	}
	// Query parameter order must not change the signature (canonical query sorts).
	a := mk("https://bedrock-runtime.us-west-2.amazonaws.com/?b=2&a=1")
	b := mk("https://bedrock-runtime.us-west-2.amazonaws.com/?a=1&b=2")
	if a != b {
		t.Errorf("query order changed signature:\n%s\n%s", a, b)
	}
}

func TestSign_CollapsesSequentialHeaderWhitespace(t *testing.T) {
	creds := awssig.Credentials{AccessKeyID: "AKID", SecretAccessKey: "secret"}
	signer := awssig.Signer{Region: "us-east-1", Service: "bedrock"}
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)

	sign := func(value string) string {
		req, _ := http.NewRequest(http.MethodPost,
			"https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude/invoke",
			strings.NewReader(`{"messages":[]}`))
		req.Header.Set("X-Amzn-Bedrock-Trace", value)
		if err := signer.Sign(req, creds, awssig.HashPayload([]byte(`{"messages":[]}`)), at); err != nil {
			t.Fatalf("sign: %v", err)
		}
		return req.Header.Get("Authorization")
	}

	normalized := sign("trace value")
	withWhitespaceRuns := sign(" \ttrace   value ")
	if normalized != withWhitespaceRuns {
		t.Errorf("sequential header whitespace changed signature:\nnormalized: %s\nruns:       %s", normalized, withWhitespaceRuns)
	}
}
