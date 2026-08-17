// Package awssig implements AWS Signature Version 4 request signing using only
// the Go standard library (crypto/hmac, crypto/sha256). It deliberately avoids
// any AWS SDK dependency to preserve the repo's minimal-deps ethos; the same
// signer is reused for both bedrock-runtime (gateway adapter) and S3 object
// storage.
//
// SigV4 reference:
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv4-create-signed-request.html
//
// Security: a Credentials value carries the AWS secret access key. The secret is
// used only inside the HMAC key-derivation chain and is NEVER written into the
// signed request, the returned headers, an error, or any other observable field.
// The only output that depends on the secret is the signature hex digest in the
// Authorization header, which is non-reversible.
package awssig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	algorithm   = "AWS4-HMAC-SHA256"
	unsignedHdr = "UNSIGNED-PAYLOAD"
)

// Credentials are the AWS credentials used to sign a request. SessionToken is
// optional (only present for temporary/STS credentials). The secret access key
// is sensitive and must not be logged.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// Valid reports whether the minimum credential material is present to sign.
func (c Credentials) Valid() bool {
	return c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// Signer signs an HTTP request for a given AWS service and region with SigV4.
type Signer struct {
	Region  string
	Service string
}

// Sign computes the SigV4 signature for req against the given payload and sets
// the Authorization, X-Amz-Date, X-Amz-Content-Sha256 (and, when present,
// X-Amz-Security-Token) headers on req. The Host header is derived from req.URL.
//
// payloadHash is the lowercase hex SHA-256 of the request body; pass
// HashPayload(body) for the common case, or UnsignedPayload() for streaming
// bodies that must not be buffered. now fixes the signing instant (use
// time.Now().UTC()); it is a parameter so tests are deterministic.
//
// Sign returns an error only for malformed inputs (no region/service, missing
// credentials, unparseable URL). It never returns the secret in the error.
func (s Signer) Sign(req *http.Request, creds Credentials, payloadHash string, now time.Time) error {
	if s.Region == "" || s.Service == "" {
		return fmt.Errorf("awssig: signer requires region and service")
	}
	if !creds.Valid() {
		return fmt.Errorf("awssig: incomplete AWS credentials")
	}
	if req.URL == nil {
		return fmt.Errorf("awssig: request has no URL")
	}
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	host := req.URL.Host
	if req.Host != "" {
		host = req.Host
	}
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}

	canonicalHeaders, signedHeaders := canonicalHeaderSet(req, host)
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, s.Region, s.Service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(creds.SecretAccessKey, dateStamp, s.Region, s.Service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	auth := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, creds.AccessKeyID, scope, signedHeaders, signature)
	req.Header.Set("Authorization", auth)
	return nil
}

// HashPayload returns the lowercase hex SHA-256 of a request body, as required
// for the X-Amz-Content-Sha256 header and the canonical request.
func HashPayload(body []byte) string { return hexSHA256(body) }

// UnsignedPayload returns the sentinel used when the body is not hashed (e.g. a
// streaming upload). The body is then excluded from the signature.
func UnsignedPayload() string { return unsignedHdr }

// canonicalHeaderSet builds the canonical headers block and the signed-headers
// list. Host plus every AWS-owned x-amz-* and x-amzn-* header present on the
// request are signed. Bedrock's request-metadata/trace/performance headers use
// the x-amzn namespace and must not be mutable after signing.
func canonicalHeaderSet(req *http.Request, host string) (canonical, signed string) {
	type kv struct{ name, value string }
	collected := map[string]string{"host": host}
	for name, values := range req.Header {
		lower := strings.ToLower(name)
		if lower == "host" || strings.HasPrefix(lower, "x-amz-") || strings.HasPrefix(lower, "x-amzn-") {
			collected[lower] = canonicalHeaderValue(strings.Join(values, ","))
		}
	}
	pairs := make([]kv, 0, len(collected))
	for name, value := range collected {
		pairs = append(pairs, kv{name, value})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].name < pairs[j].name })

	var b strings.Builder
	names := make([]string, 0, len(pairs))
	for _, p := range pairs {
		b.WriteString(p.name)
		b.WriteByte(':')
		b.WriteString(p.value)
		b.WriteByte('\n')
		names = append(names, p.name)
	}
	return b.String(), strings.Join(names, ";")
}

// canonicalHeaderValue applies AWS SigV4's Trimall rule: leading/trailing
// whitespace is removed and every sequential whitespace run becomes one
// ASCII space. The request header itself is left untouched; only the canonical
// representation used to compute the signature is normalized.
func canonicalHeaderValue(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// canonicalURI returns the URI-encoded path. AWS does not re-encode an already
// path-escaped URL except for the bedrock model id; using EscapedPath keeps any
// pre-encoded segments intact, matching what the client actually sends.
func canonicalURI(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

// canonicalQuery returns the query string sorted by key with each key and value
// URI-encoded, per the SigV4 canonical query rules.
func canonicalQuery(u *url.URL) string {
	values := u.Query()
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		vs := values[k]
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, awsURIEncode(k, true)+"="+awsURIEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

// awsURIEncode encodes per RFC 3986, additionally encoding "/" when encodeSlash
// is true (the rule for query components and non-S3 path components).
func awsURIEncode(s string, encodeSlash bool) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case strings.IndexByte(unreserved, c) >= 0:
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
