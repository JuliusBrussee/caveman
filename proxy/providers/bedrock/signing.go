package bedrock

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/awssig"
)

// SanitizeAndMapHeaders builds the upstream header set for Bedrock Runtime or
// Mantle. Bedrock API keys use a bearer on Runtime and x-api-key on Mantle. IAM
// access keys are SigV4-signed with the endpoint's distinct service name.
//
// IAM credentials retain the legacy "accessKeyId:secretAccessKey[:sessionToken]"
// encoding at the adapter boundary. The secret is consumed only to derive the
// signature and never copied into a forwarded header, log, error, or telemetry.
func (a Adapter) SanitizeAndMapHeaders(ctx context.Context, req *http.Request, credential providers.Credential, upstream *url.URL) (http.Header, error) {
	out := http.Header{}
	copyIfPresent(out, req.Header, "content-type")
	copyIfPresent(out, req.Header, "accept")
	mantle := endpointKindForPath(req.URL.Path) == endpointMantle
	if mantle {
		copyIfPresent(out, req.Header, "anthropic-version")
		copyIfPresent(out, req.Header, "anthropic-beta")
	} else {
		copyIfPresent(out, req.Header, "x-amzn-bedrock-accept")
		copyIfPresent(out, req.Header, "x-amzn-bedrock-guardrail-identifier")
		copyIfPresent(out, req.Header, "x-amzn-bedrock-guardrail-version")
		copyIfPresent(out, req.Header, "x-amzn-bedrock-performanceconfig-latency")
		copyIfPresent(out, req.Header, "x-amzn-bedrock-request-metadata")
		copyIfPresent(out, req.Header, "x-amzn-bedrock-service-tier")
		copyIfPresent(out, req.Header, "x-amzn-bedrock-trace")
	}
	if out.Get("content-type") == "" {
		out.Set("content-type", "application/json")
	}

	authKind, err := credentialAuthKind(credential)
	if err != nil {
		return nil, err
	}
	if authKind == "bedrock_api_key" {
		key := strings.TrimSpace(credential.Key)
		if key == "" || strings.ContainsAny(key, "\r\n") {
			return nil, fmt.Errorf("bedrock: invalid API key")
		}
		if mantle {
			out.Set("x-api-key", key)
		} else {
			out.Set("Authorization", "Bearer "+key)
		}
		return out, nil
	}

	creds, err := parseAWSCredentials(credential.Key)
	if err != nil {
		return nil, err
	}

	// Sign against the SAME URL the proxy will forward to. The caller passes the
	// already-resolved upstream (which honors a per-project base URL); re-resolving
	// here with an empty route would sign the adapter's fallback host instead, so a
	// project with a custom Bedrock endpoint would get a SigV4 Host mismatch
	// (SignatureDoesNotMatch). Fall back to a self-resolve only if the caller passed
	// nil (no forward URL available).
	if upstream == nil {
		upstream, err = a.ResolveUpstreamURL(ctx, req, providers.RouteContext{})
		if err != nil {
			return nil, err
		}
	}

	// Build a synthetic request carrying the upstream host/path/query and the
	// to-be-signed headers, sign it, then merge the signed headers into out. This
	// keeps the signing surface (host, path, query, x-amz-*) identical to what the
	// proxy actually sends upstream.
	toSign, err := http.NewRequestWithContext(ctx, req.Method, upstream.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("bedrock: could not build signing request: %w", err)
	}
	toSign.Header = out.Clone()

	signingService := runtimeService
	if mantle {
		signingService = mantleService
	}
	payloadHash, err := requestPayloadHash(ctx, req)
	if err != nil {
		return nil, err
	}
	signer := awssig.Signer{Region: signingRegion(req, upstream), Service: signingService}
	if err := signer.Sign(toSign, creds, payloadHash, time.Now()); err != nil {
		// The error from Sign never contains the secret (see awssig docs); still,
		// return a generic mapping error rather than the raw text.
		return nil, fmt.Errorf("bedrock: request signing failed")
	}

	for _, name := range []string{"Authorization", "Host", "X-Amz-Date", "X-Amz-Content-Sha256", "X-Amz-Security-Token"} {
		if v := toSign.Header.Get(name); v != "" {
			out.Set(name, v)
		}
	}
	return out, nil
}

// requestPayloadHash returns the hash of the exact post-transform wire body.
// Gateways install it in context after transforming. Direct adapter callers and
// verification probes can fall back to net/http's replayable GetBody contract.
// A non-replayable body without a bound hash fails closed instead of emitting a
// signature over bytes that may differ from the request on the wire.
func requestPayloadHash(ctx context.Context, req *http.Request) (string, error) {
	if hash, ok := providers.RequestPayloadHash(ctx); ok {
		return hash, nil
	}
	if req.Body == nil {
		return awssig.HashPayload(nil), nil
	}
	if req.GetBody == nil {
		return "", fmt.Errorf("bedrock: exact request payload hash is unavailable")
	}
	body, err := req.GetBody()
	if err != nil {
		return "", fmt.Errorf("bedrock: exact request payload hash is unavailable")
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("bedrock: exact request payload hash is unavailable")
	}
	return awssig.HashPayload(raw), nil
}

func credentialAuthKind(credential providers.Credential) (string, error) {
	kind := strings.ToLower(strings.TrimSpace(credential.AuthKind))
	switch kind {
	case "bedrock_api_key", "aws_access_keys":
		return kind, nil
	case "":
		key := strings.TrimSpace(credential.Key)
		if key == "" {
			return "", fmt.Errorf("bedrock: missing credential")
		}
		// Backward compatibility for existing x-cave-upstream-key and stored
		// connection values. A complete colon-form credential remains IAM. An
		// AKIA/ASIA-looking partial value fails as IAM instead of being sent as a
		// bearer. Every other opaque value is a Bedrock API key.
		if _, err := parseAWSCredentials(key); err == nil {
			return "aws_access_keys", nil
		}
		if strings.HasPrefix(key, "AKIA") || strings.HasPrefix(key, "ASIA") {
			return "aws_access_keys", nil
		}
		return "bedrock_api_key", nil
	default:
		return "", fmt.Errorf("bedrock: unsupported credential auth kind")
	}
}

// parseAWSCredentials decodes the "accessKeyId:secretAccessKey[:sessionToken]"
// form carried in x-cave-upstream-key into awssig.Credentials. It fails closed:
// a missing access key or secret is an error, never an unsigned passthrough.
func parseAWSCredentials(raw string) (awssig.Credentials, error) {
	if raw == "" {
		return awssig.Credentials{}, fmt.Errorf("bedrock: missing AWS credentials in x-cave-upstream-key")
	}
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return awssig.Credentials{}, fmt.Errorf("bedrock: malformed AWS credentials (want accessKeyId:secretAccessKey[:sessionToken])")
	}
	creds := awssig.Credentials{AccessKeyID: parts[0], SecretAccessKey: parts[1]}
	if len(parts) == 3 {
		creds.SessionToken = parts[2]
	}
	return creds, nil
}

// copyIfPresent copies a header from src to dst when present (case-insensitive).
func copyIfPresent(dst, src http.Header, name string) {
	if values := src.Values(name); len(values) > 0 {
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}
