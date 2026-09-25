// Package awscreds resolves AWS credentials for SigV4 signing from the ambient
// container/instance environment, using only the Go standard library. It exists
// for the same reason as the sibling awssig package: the proxy signs Bedrock
// Runtime requests without taking an AWS SDK dependency, so the default
// credential chain has to be re-implemented in the few hundred lines that
// actually matter to a proxy deployed inside a VPC.
//
// Chain order, first source that yields credentials wins:
//
//  1. env          — AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY (+ AWS_SESSION_TOKEN)
//  2. web_identity — AWS_WEB_IDENTITY_TOKEN_FILE + AWS_ROLE_ARN (EKS IRSA), via
//     an unsigned sts:AssumeRoleWithWebIdentity call
//  3. container    — AWS_CONTAINER_CREDENTIALS_FULL_URI or
//     AWS_CONTAINER_CREDENTIALS_RELATIVE_URI (ECS task role, EKS Pod Identity)
//  4. imds         — IMDSv2 only (EC2 instance profile)
//
// The shared config and credentials files (~/.aws/...) are deliberately not in
// the chain: a container does not have them, and reading a developer's ambient
// profile inside a server process is a surprise, not a feature.
//
// Security: the returned secret access key and session token, the web identity
// token, and the container authorization token are never placed in an error, a
// log line, or any other observable field. Upstream failures are reported with
// the HTTP status and, for STS, the error <Code> only — a response body can echo
// the token that was sent.
package awscreds

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/awssig"
)

const (
	defaultRegion = "us-east-1"
	// refreshWindow is how long before expiry a cached credential is renewed.
	refreshWindow = 5 * time.Minute
	// negativeTTL caches a chain failure so a burst of Bedrock requests on a box
	// with no credentials cannot hammer IMDS once per request.
	negativeTTL = 10 * time.Second
	// fetchTimeout bounds ONE walk of the whole chain. The walk runs under p.mu
	// on the request path, so without it a stalled STS or metadata endpoint
	// serialized every Bedrock request behind the sum of four source timeouts
	// (~16s). Three seconds is longer than any healthy link-local or STS hop and
	// short enough that a hung endpoint degrades to a fast failure.
	fetchTimeout = 3 * time.Second
	// maxBody bounds every credential response we parse.
	maxBody = 1 << 20

	defaultContainerBase = "http://169.254.170.2"
	defaultIMDSBase      = "http://169.254.169.254"
)

// Options configures a Provider. The zero value is usable: it reads the real
// process environment and the real clock.
type Options struct {
	// Region is the signing/STS region. Empty falls back to AWS_REGION, then
	// AWS_DEFAULT_REGION, then us-east-1.
	Region string
	// STSEndpoint overrides the STS base URL, for tests and for VPC interface
	// endpoints. Empty means https://sts.<region>.amazonaws.com.
	STSEndpoint string
	// HTTPClient is used for STS only. The link-local metadata endpoints always
	// use an internal client that never honours a proxy. Empty means a client
	// with a 10s timeout and http.ProxyFromEnvironment.
	HTTPClient *http.Client
	// Getenv defaults to os.Getenv.
	Getenv func(string) string
	// Now defaults to time.Now.
	Now func() time.Time
}

// Provider resolves and caches AWS credentials. It is safe for concurrent use.
type Provider struct {
	region      string
	stsEndpoint string
	sts         *http.Client
	link        *http.Client
	getenv      func(string) string
	now         func() time.Time
	// containerBase resolves AWS_CONTAINER_CREDENTIALS_RELATIVE_URI. Overridden
	// only by tests; the real value is fixed by ECS.
	containerBase string

	// mu serializes both the cache and the fetch itself: a concurrent caller
	// blocks on the in-flight refresh and then reads its result from the cache.
	// fetchTimeout caps how long that block can last.
	// ponytail: one lock for one credential set; nothing here needs finer grain.
	mu       sync.Mutex
	creds    awssig.Credentials
	expires  time.Time // zero means the credentials never expire
	source   string
	err      error
	errUntil time.Time
}

// New builds a Provider from opts.
func New(opts Options) *Provider {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	region := strings.TrimSpace(opts.Region)
	if region == "" {
		region = strings.TrimSpace(getenv("AWS_REGION"))
	}
	if region == "" {
		region = strings.TrimSpace(getenv("AWS_DEFAULT_REGION"))
	}
	if region == "" {
		region = defaultRegion
	}
	sts := opts.HTTPClient
	if sts == nil {
		// http.DefaultTransport already carries http.ProxyFromEnvironment.
		sts = &http.Client{Timeout: 10 * time.Second}
	}
	endpoint := strings.TrimSuffix(strings.TrimSpace(opts.STSEndpoint), "/")
	if endpoint == "" {
		endpoint = "https://sts." + region + ".amazonaws.com"
	}
	return &Provider{
		region:      region,
		stsEndpoint: endpoint,
		sts:         sts,
		// Link-local credential endpoints must never be routed through a proxy,
		// and a box that is not on EC2 has to fail fast rather than stall the
		// request that triggered the lookup.
		link: &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				Proxy:       nil,
				DialContext: (&net.Dialer{Timeout: time.Second}).DialContext,
			},
		},
		getenv:        getenv,
		now:           now,
		containerBase: defaultContainerBase,
	}
}

// Region reports the resolved signing region.
func (p *Provider) Region() string { return p.region }

// Source reports the chain entry that last produced credentials: "env",
// "web_identity", "container", "imds", or "" before the first success.
func (p *Provider) Source() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.source
}

// Credentials returns credentials for signing, refreshing them when they are
// within five minutes of expiry.
func (p *Provider) Credentials(ctx context.Context) (awssig.Credentials, error) {
	// A caller that has already hung up gets its own error, not the cache and
	// not a queue slot behind someone else's refresh.
	if err := ctx.Err(); err != nil {
		return awssig.Credentials{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if p.fresh(now) {
		return p.creds, nil
	}
	if now.Before(p.errUntil) {
		if p.err != nil {
			return awssig.Credentials{}, p.err
		}
		// A refresh failed recently but the cached credentials are still valid:
		// keep serving them without re-dialing the metadata endpoint on every
		// request (the fetch runs under p.mu, so each attempt would serialize
		// every caller behind a dial timeout).
		if p.creds.Valid() && (p.expires.IsZero() || now.Before(p.expires)) {
			return p.creds, nil
		}
	}
	// The fetch is shared: every concurrent caller is queued on p.mu waiting for
	// this one walk, so it must not be abandoned because the caller that happened
	// to win the lock hung up -- the queue would then re-dial the chain once per
	// waiter. Detaching it also means the 3s bound is the only bound, which is
	// what the waiters already face: a mutex does not honour a context.
	fetchCtx, cancelFetch := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	res, err := p.fetch(fetchCtx)
	cancelFetch()
	// Because the fetch is detached, err is always about the credential source
	// and is safe to cache; a caller cancelling can no longer masquerade as an
	// endpoint failure. The caller's own cancellation is a separate question,
	// and it must not be answered by a race: whether the response landed before
	// cancel() reached the transport decided the return value at random. A
	// caller that hung up now always gets its own ctx.Err(), while what the
	// fetch found is still recorded for whoever is queued behind it.
	callerErr := ctx.Err()
	if err != nil {
		// Credentials that are stale-but-still-valid beat a hard failure: the
		// refresh window exists so a flaky metadata endpoint has five minutes of
		// retries before it can break signing.
		if p.creds.Valid() && (p.expires.IsZero() || now.Before(p.expires)) {
			p.errUntil = now.Add(negativeTTL)
			if callerErr != nil {
				return awssig.Credentials{}, callerErr
			}
			return p.creds, nil
		}
		p.err, p.errUntil = err, now.Add(negativeTTL)
		if callerErr != nil {
			return awssig.Credentials{}, callerErr
		}
		return awssig.Credentials{}, err
	}
	if res.expires.IsZero() && res.creds.SessionToken != "" {
		// A session token is temporary even when the endpoint omitted Expiration.
		// Pinning it forever would leave signing broken once it lapses upstream
		// with no refresh ever attempted.
		res.expires = now.Add(15 * time.Minute)
	}
	p.creds, p.expires, p.source = res.creds, res.expires, res.source
	p.err, p.errUntil = nil, time.Time{}
	if callerErr != nil {
		return awssig.Credentials{}, callerErr
	}
	return p.creds, nil
}

func (p *Provider) fresh(now time.Time) bool {
	if !p.creds.Valid() {
		return false
	}
	return p.expires.IsZero() || p.expires.Sub(now) >= refreshWindow
}

// result is one chain entry's answer. A nil *result with a nil error means the
// source is not configured on this host, so the chain moves on.
type result struct {
	creds   awssig.Credentials
	expires time.Time
	source  string
}

func (p *Provider) fetch(ctx context.Context) (*result, error) {
	for _, source := range []func(context.Context) (*result, error){
		p.fromEnv, p.fromWebIdentity, p.fromContainer, p.fromIMDS,
	} {
		res, err := source(ctx)
		if err != nil {
			return nil, err
		}
		if res == nil {
			continue
		}
		if !res.creds.Valid() {
			return nil, fmt.Errorf("awscreds: %s returned incomplete credentials", res.source)
		}
		return res, nil
	}
	return nil, errors.New("awscreds: no AWS credentials found (env, web identity, container, IMDS)")
}

func (p *Provider) env(name string) string { return strings.TrimSpace(p.getenv(name)) }

// fromEnv reads static keys. A half-configured pair is an error, not a skip:
// the operator clearly meant to sign as these keys, and falling through would
// silently sign as whatever ambient role the host carries — a different
// principal, bill, and CloudTrail identity — with no disclosure. A lone
// AWS_SESSION_TOKEN is not a pair and does not trigger this.
func (p *Provider) fromEnv(context.Context) (*result, error) {
	access, secret := p.env("AWS_ACCESS_KEY_ID"), p.env("AWS_SECRET_ACCESS_KEY")
	if access == "" && secret == "" {
		return nil, nil
	}
	if access == "" || secret == "" {
		return nil, errors.New("awscreds: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must both be set")
	}
	return &result{
		creds: awssig.Credentials{
			AccessKeyID:     access,
			SecretAccessKey: secret,
			SessionToken:    p.env("AWS_SESSION_TOKEN"),
		},
		source: "env",
	}, nil
}

// fromWebIdentity implements the EKS IRSA / generic OIDC flow: exchange the
// projected service account token for role credentials at STS. The call is
// unsigned by definition — the token is the proof.
func (p *Provider) fromWebIdentity(ctx context.Context) (*result, error) {
	tokenFile, roleARN := p.env("AWS_WEB_IDENTITY_TOKEN_FILE"), p.env("AWS_ROLE_ARN")
	if tokenFile == "" || roleARN == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("awscreds: read web identity token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, errors.New("awscreds: web identity token file is empty")
	}
	sessionName := p.env("AWS_ROLE_SESSION_NAME")
	if sessionName == "" {
		sessionName = fmt.Sprintf("caveman-proxy-%d", p.now().Unix())
	}
	form := url.Values{
		"Action":           {"AssumeRoleWithWebIdentity"},
		"Version":          {"2011-06-15"},
		"RoleArn":          {roleARN},
		"RoleSessionName":  {sessionName},
		"WebIdentityToken": {token},
		"DurationSeconds":  {"3600"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.stsEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("awscreds: build sts request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/xml")
	resp, err := p.sts.Do(req)
	if err != nil {
		// A transport error can carry the request URL but never the form body.
		return nil, fmt.Errorf("awscreds: sts assume role with web identity failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("awscreds: read sts response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("awscreds: sts assume role with web identity: http %d%s", resp.StatusCode, stsErrorCode(body))
	}
	var parsed struct {
		XMLName xml.Name `xml:"AssumeRoleWithWebIdentityResponse"`
		Result  struct {
			Credentials struct {
				AccessKeyID     string `xml:"AccessKeyId"`
				SecretAccessKey string `xml:"SecretAccessKey"`
				SessionToken    string `xml:"SessionToken"`
				Expiration      string `xml:"Expiration"`
			} `xml:"Credentials"`
		} `xml:"AssumeRoleWithWebIdentityResult"`
	}
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return nil, errors.New("awscreds: sts returned an unparseable response")
	}
	c := parsed.Result.Credentials
	expires, err := parseExpiry(c.Expiration)
	if err != nil {
		return nil, fmt.Errorf("awscreds: sts credential expiry: %w", err)
	}
	return &result{
		creds: awssig.Credentials{
			AccessKeyID:     strings.TrimSpace(c.AccessKeyID),
			SecretAccessKey: strings.TrimSpace(c.SecretAccessKey),
			SessionToken:    strings.TrimSpace(c.SessionToken),
		},
		expires: expires,
		source:  "web_identity",
	}, nil
}

// stsErrorCode extracts the machine-readable code of an STS ErrorResponse. The
// body itself is never returned: it can echo the web identity token.
func stsErrorCode(body []byte) string {
	var parsed struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		Error   struct {
			Code string `xml:"Code"`
		} `xml:"Error"`
	}
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	code := strings.TrimSpace(parsed.Error.Code)
	if code == "" {
		return ""
	}
	return " (" + code + ")"
}

// credentialJSON is the shape both the ECS/Pod Identity endpoint and IMDS
// return. Code is only populated by IMDS.
type credentialJSON struct {
	Code            string `json:"Code"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	Token           string `json:"Token"`
	Expiration      string `json:"Expiration"`
}

func (p *Provider) fromContainer(ctx context.Context) (*result, error) {
	endpoint := p.env("AWS_CONTAINER_CREDENTIALS_FULL_URI")
	if endpoint != "" {
		if err := checkContainerURI(endpoint); err != nil {
			return nil, err
		}
	} else {
		relative := p.env("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI")
		if relative == "" {
			return nil, nil
		}
		if !strings.HasPrefix(relative, "/") {
			relative = "/" + relative
		}
		endpoint = strings.TrimSuffix(p.containerBase, "/") + relative
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("awscreds: build container credentials request: %w", err)
	}
	auth, err := p.containerAuthToken()
	if err != nil {
		return nil, err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Accept", "application/json")
	body, err := p.doJSON(p.link, req, "container credentials")
	if err != nil {
		return nil, err
	}
	return credentialsFromJSON(body, "container")
}

func (p *Provider) containerAuthToken() (string, error) {
	if file := p.env("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"); file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("awscreds: read container authorization token file: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return p.env("AWS_CONTAINER_AUTHORIZATION_TOKEN"), nil
}

// containerCredentialHosts is the fixed set of non-loopback addresses the AWS
// SDKs will talk to in PLAINTEXT for container credentials: the ECS task-role
// endpoint and EKS Pod Identity (v4 and v6). Accepting all of 169.254.0.0/16 and
// fe80::/10 — every link-local address — instead meant any neighbouring
// link-local listener could be handed the task role's Authorization token.
var containerCredentialHosts = []netip.Addr{
	netip.MustParseAddr("169.254.170.2"),  // ECS task role
	netip.MustParseAddr("169.254.170.23"), // EKS Pod Identity
	netip.MustParseAddr("fd00:ec2::23"),   // EKS Pod Identity over IPv6
}

// imdsHosts is the same idea for the instance metadata service.
var imdsHosts = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"),
	netip.MustParseAddr("fd00:ec2::254"),
}

// plaintextHostAllowed reports whether host may be reached over plain HTTP by a
// credential lookup: loopback, or one of the fixed metadata addresses in allow.
func plaintextHostAllowed(host string, allow []netip.Addr) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	if addr.IsLoopback() {
		return true
	}
	addr = addr.Unmap()
	for _, allowed := range allow {
		if addr == allowed {
			return true
		}
	}
	return false
}

// checkContainerURI applies the SDK rule for a caller-supplied credential
// endpoint: TLS anywhere, plaintext only to loopback or the fixed ECS/EKS
// credential addresses. Without it, AWS_CONTAINER_CREDENTIALS_FULL_URI is a
// request to hand a task role's Authorization token to an arbitrary host.
func checkContainerURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("awscreds: AWS_CONTAINER_CREDENTIALS_FULL_URI is not a valid URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if plaintextHostAllowed(u.Hostname(), containerCredentialHosts) {
			return nil
		}
		return fmt.Errorf("awscreds: refusing plaintext container credentials endpoint at host %q (allowed: loopback, 169.254.170.2, 169.254.170.23, fd00:ec2::23)", u.Hostname())
	default:
		return fmt.Errorf("awscreds: unsupported container credentials scheme %q", u.Scheme)
	}
}

// checkIMDSEndpoint is checkContainerURI for AWS_EC2_METADATA_SERVICE_ENDPOINT.
// That variable was taken verbatim and then dialled with p.link — the client
// that deliberately ignores every proxy setting — so any host named there became
// a proxy-bypassing outbound request with the IMDSv2 token attached.
func checkIMDSEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("awscreds: AWS_EC2_METADATA_SERVICE_ENDPOINT is not a valid URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if plaintextHostAllowed(u.Hostname(), imdsHosts) {
			return nil
		}
		return fmt.Errorf("awscreds: refusing plaintext IMDS endpoint at host %q (allowed: loopback, 169.254.169.254, fd00:ec2::254)", u.Hostname())
	default:
		return fmt.Errorf("awscreds: unsupported IMDS endpoint scheme %q", u.Scheme)
	}
}

func (p *Provider) fromIMDS(ctx context.Context) (*result, error) {
	if strings.EqualFold(p.env("AWS_EC2_METADATA_DISABLED"), "true") {
		return nil, nil
	}
	base := p.env("AWS_EC2_METADATA_SERVICE_ENDPOINT")
	if base == "" {
		base = defaultIMDSBase
	}
	if err := checkIMDSEndpoint(base); err != nil {
		return nil, err
	}
	base = strings.TrimSuffix(base, "/")

	// IMDSv2 only: a v1 fallback would leave the proxy vulnerable to the SSRF
	// class the session token exists to close.
	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/latest/api/token", nil)
	if err != nil {
		return nil, fmt.Errorf("awscreds: build imds token request: %w", err)
	}
	// One minute: this token authorizes the two metadata GETs immediately below
	// and is then dropped. The six-hour maximum only widens the window in which a
	// leaked token is still usable.
	tokenReq.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")
	tokenBody, err := p.doJSON(p.link, tokenReq, "imds token")
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(tokenBody))
	if token == "" {
		return nil, errors.New("awscreds: imds returned an empty session token")
	}

	roleBody, err := p.imdsGet(ctx, base+"/latest/meta-data/iam/security-credentials/", token, "imds role")
	if err != nil {
		return nil, err
	}
	role := ""
	for _, line := range strings.Split(string(roleBody), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			role = line
			break
		}
	}
	if role == "" {
		return nil, errors.New("awscreds: no instance profile role attached")
	}

	credBody, err := p.imdsGet(ctx, base+"/latest/meta-data/iam/security-credentials/"+url.PathEscape(role), token, "imds credentials")
	if err != nil {
		return nil, err
	}
	return credentialsFromJSON(credBody, "imds")
}

func (p *Provider) imdsGet(ctx context.Context, endpoint, token, what string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("awscreds: build %s request: %w", what, err)
	}
	req.Header.Set("X-aws-ec2-metadata-token", token)
	return p.doJSON(p.link, req, what)
}

// doJSON performs one attempt and returns the bounded body. A non-2xx response
// is reported by status only: a metadata body holds credential material.
func (p *Provider) doJSON(client *http.Client, req *http.Request, what string) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("awscreds: %s request failed: %w", what, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("awscreds: read %s response: %w", what, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("awscreds: %s: http %d", what, resp.StatusCode)
	}
	return body, nil
}

func credentialsFromJSON(body []byte, source string) (*result, error) {
	var parsed credentialJSON
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("awscreds: %s returned an unparseable response", source)
	}
	if parsed.Code != "" && !strings.EqualFold(parsed.Code, "Success") {
		return nil, fmt.Errorf("awscreds: %s returned code %q", source, parsed.Code)
	}
	expires, err := parseExpiry(parsed.Expiration)
	if err != nil {
		return nil, fmt.Errorf("awscreds: %s credential expiry: %w", source, err)
	}
	return &result{
		creds: awssig.Credentials{
			AccessKeyID:     strings.TrimSpace(parsed.AccessKeyID),
			SecretAccessKey: strings.TrimSpace(parsed.SecretAccessKey),
			SessionToken:    strings.TrimSpace(parsed.Token),
		},
		expires: expires,
		source:  source,
	}, nil
}

// parseExpiry maps an absent expiry to the zero time, which means "never
// refresh" to the cache.
func parseExpiry(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, errors.New("not an RFC3339 timestamp")
	}
	return t, nil
}
