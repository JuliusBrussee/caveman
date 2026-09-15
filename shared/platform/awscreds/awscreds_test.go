package awscreds

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newProvider builds a Provider over a fixed environment map and a fake clock.
func newProvider(t *testing.T, environ map[string]string, opts Options) (*Provider, *clock) {
	t.Helper()
	clk := newClock()
	opts.Getenv = func(k string) string { return environ[k] }
	opts.Now = clk.now
	return New(opts), clk
}

// failTransport fails the test if any request escapes the provider.
type failTransport struct {
	t      *testing.T
	called atomic.Bool
}

func (f *failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.called.Store(true)
	f.t.Errorf("unexpected request to %s", r.URL)
	return nil, errors.New("unexpected request")
}

func credJSON(expiry time.Time) string {
	return fmt.Sprintf(`{"Code":"Success","AccessKeyId":"AKIDCONTAINER","SecretAccessKey":"secret-container","Token":"token-container","Expiration":%q}`,
		expiry.Format(time.RFC3339))
}

func TestEnvWinsOverEverything(t *testing.T) {
	blocked := &failTransport{t: t}
	p, _ := newProvider(t, map[string]string{
		"AWS_ACCESS_KEY_ID":                      " AKIDENV ",
		"AWS_SECRET_ACCESS_KEY":                  " secret-env ",
		"AWS_SESSION_TOKEN":                      " token-env ",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "/creds",
	}, Options{})
	p.link = &http.Client{Transport: blocked}

	got, err := p.Credentials(context.Background())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if got.AccessKeyID != "AKIDENV" || got.SecretAccessKey != "secret-env" || got.SessionToken != "token-env" {
		t.Fatalf("unexpected credentials: %+v", got)
	}
	if p.Source() != "env" {
		t.Fatalf("Source = %q, want env", p.Source())
	}
}

func TestPartialEnvPairFailsClosed(t *testing.T) {
	clk := newClock()
	p := New(Options{
		Getenv: func(k string) string {
			return map[string]string{
				// Secret missing: the operator meant these keys, so the chain must
				// not quietly sign as the container role instead.
				"AWS_ACCESS_KEY_ID":                  "AKIDENV",
				"AWS_CONTAINER_CREDENTIALS_FULL_URI": "http://127.0.0.1:1/creds",
			}[k]
		},
		Now: clk.now,
	})
	_, err := p.Credentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("err = %v, want the partial pair named", err)
	}
	if p.Source() != "" {
		t.Fatalf("Source = %q, want no source after a partial pair", p.Source())
	}
}

func TestLoneSessionTokenIsNotAPair(t *testing.T) {
	p, _, fetches, done := containerProvider(t)
	defer done()
	base := p.getenv
	p.getenv = func(k string) string {
		if k == "AWS_SESSION_TOKEN" {
			return "orphan-session-token"
		}
		return base(k)
	}
	if _, err := p.Credentials(context.Background()); err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if fetches.Load() != 1 || p.Source() != "container" {
		t.Fatalf("fetches = %d source = %q, want the container role", fetches.Load(), p.Source())
	}
}

func TestContainerRelativeURIAndTokenFile(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("  Bearer container-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clk := newClock()
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		fmt.Fprint(w, credJSON(clk.now().Add(time.Hour)))
	}))
	defer srv.Close()

	p := New(Options{
		Getenv: func(k string) string {
			return map[string]string{
				"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "/v2/credentials/abc123",
				"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": tokenFile,
			}[k]
		},
		Now: clk.now,
	})
	p.containerBase = srv.URL

	got, err := p.Credentials(context.Background())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if got.SessionToken != "token-container" {
		t.Fatalf("session token = %q", got.SessionToken)
	}
	if gotPath != "/v2/credentials/abc123" {
		t.Fatalf("path = %q, want the relative URI resolved against the base", gotPath)
	}
	if gotAuth != "Bearer container-secret" {
		t.Fatalf("Authorization = %q, want the trimmed token file contents", gotAuth)
	}
}

func TestContainerRejectsPlaintextNonLoopbackHost(t *testing.T) {
	blocked := &failTransport{t: t}
	p, _ := newProvider(t, map[string]string{
		"AWS_CONTAINER_CREDENTIALS_FULL_URI": "http://evil.example.com/creds",
		"AWS_EC2_METADATA_DISABLED":          "true",
	}, Options{})
	p.link = &http.Client{Transport: blocked}

	if _, err := p.Credentials(context.Background()); err == nil {
		t.Fatal("expected a rejection for a plaintext non-loopback endpoint")
	} else if !strings.Contains(err.Error(), "evil.example.com") {
		t.Fatalf("error = %v, want it to name the rejected host", err)
	}
	if blocked.called.Load() {
		t.Fatal("a request was made to the rejected endpoint")
	}
}

// The plaintext allowlist is the three addresses the AWS SDKs actually use, not
// every link-local address: 169.254.0.0/16 and fe80::/10 are shared with any
// other listener on the link, and the container endpoint is handed an
// Authorization token.
func TestContainerPlaintextAllowlistIsECSAndEKSOnly(t *testing.T) {
	for _, endpoint := range []string{
		"http://169.254.170.2/creds",  // ECS task role
		"http://169.254.170.23/creds", // EKS Pod Identity
		"http://[fd00:ec2::23]/creds", // EKS Pod Identity over IPv6
		"http://127.0.0.1:8080/creds", "http://localhost/creds", "http://[::1]/creds",
		"https://creds.example.com/",
	} {
		if err := checkContainerURI(endpoint); err != nil {
			t.Errorf("checkContainerURI(%q) = %v, want nil", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"http://169.254.169.254/creds", // IMDS is not a container credential source
		"http://169.254.1.1/creds",     // some other tenant of the link
		"http://[fe80::1]/creds",
		"http://10.0.0.5/creds", "ftp://169.254.170.2/", "http://%zz/",
	} {
		if err := checkContainerURI(endpoint); err == nil {
			t.Errorf("checkContainerURI(%q) = nil, want an error", endpoint)
		}
	}
}

// AWS_EC2_METADATA_SERVICE_ENDPOINT was taken verbatim and then dialled with the
// client that ignores every proxy setting.
func TestIMDSEndpointAllowlist(t *testing.T) {
	for _, endpoint := range []string{
		"http://169.254.169.254", "http://[fd00:ec2::254]", "http://127.0.0.1:1234",
		"http://localhost:8080", "https://imds.example.internal",
	} {
		if err := checkIMDSEndpoint(endpoint); err != nil {
			t.Errorf("checkIMDSEndpoint(%q) = %v, want nil", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"http://169.254.170.2", "http://169.254.169.253", "http://[fe80::1]",
		"http://metadata.example.com", "http://10.0.0.5", "ftp://169.254.169.254",
		"not-a-url",
	} {
		if err := checkIMDSEndpoint(endpoint); err == nil {
			t.Errorf("checkIMDSEndpoint(%q) = nil, want an error", endpoint)
		}
	}
}

// A rejected endpoint must be rejected before anything is dialled.
func TestIMDSEndpointRejectionNeverDials(t *testing.T) {
	blocked := &failTransport{t: t}
	p, _ := newProvider(t, map[string]string{
		"AWS_EC2_METADATA_SERVICE_ENDPOINT": "http://metadata.attacker.example",
	}, Options{})
	p.link = &http.Client{Transport: blocked}

	if _, err := p.Credentials(context.Background()); err == nil {
		t.Fatal("expected a rejection for a non-metadata IMDS endpoint")
	} else if !strings.Contains(err.Error(), "metadata.attacker.example") {
		t.Fatalf("error = %v, want it to name the rejected host", err)
	}
	if blocked.called.Load() {
		t.Fatal("a request was made to the rejected endpoint")
	}
}

// imdsStub serves IMDSv2 and refuses any metadata GET that arrives without the
// session token, so a provider that skipped the PUT cannot pass.
func imdsStub(t *testing.T, clk *clock, servePUT bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/latest/api/token" {
			if !servePUT {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// The token covers two GETs and is then dropped; a six-hour TTL only
			// widened the window in which a leaked one still worked.
			if r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") != "60" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, "imds-session-token")
			return
		}
		gets.Add(1)
		if r.Header.Get("X-aws-ec2-metadata-token") != "imds-session-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/latest/meta-data/iam/security-credentials/":
			fmt.Fprint(w, "\ncaveman-proxy-role\n")
		case "/latest/meta-data/iam/security-credentials/caveman-proxy-role":
			fmt.Fprint(w, credJSON(clk.now().Add(time.Hour)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, &gets
}

func TestIMDSv2Sequence(t *testing.T) {
	clk := newClock()
	srv, gets := imdsStub(t, clk, true)
	defer srv.Close()

	p := New(Options{
		Getenv: func(k string) string {
			return map[string]string{"AWS_EC2_METADATA_SERVICE_ENDPOINT": srv.URL}[k]
		},
		Now: clk.now,
	})
	got, err := p.Credentials(context.Background())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if got.AccessKeyID != "AKIDCONTAINER" || got.SessionToken != "token-container" {
		t.Fatalf("unexpected credentials: %+v", got)
	}
	if p.Source() != "imds" {
		t.Fatalf("Source = %q, want imds", p.Source())
	}
	if n := gets.Load(); n != 2 {
		t.Fatalf("metadata GETs = %d, want 2 (role list, then role)", n)
	}
}

func TestIMDSWithoutTokenEndpointFails(t *testing.T) {
	clk := newClock()
	srv, gets := imdsStub(t, clk, false)
	defer srv.Close()

	p := New(Options{
		Getenv: func(k string) string {
			return map[string]string{"AWS_EC2_METADATA_SERVICE_ENDPOINT": srv.URL}[k]
		},
		Now: clk.now,
	})
	_, err := p.Credentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "imds token") {
		t.Fatalf("error = %v, want an IMDSv2 token failure", err)
	}
	if gets.Load() != 0 {
		t.Fatal("provider fell back to an unauthenticated IMDSv1 GET")
	}
}

func TestIMDSDisabledSkipsEndpoint(t *testing.T) {
	blocked := &failTransport{t: t}
	p, _ := newProvider(t, map[string]string{
		"AWS_EC2_METADATA_DISABLED":         "TRUE",
		"AWS_EC2_METADATA_SERVICE_ENDPOINT": "http://169.254.169.254",
	}, Options{})
	p.link = &http.Client{Transport: blocked}

	_, err := p.Credentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no AWS credentials found") {
		t.Fatalf("error = %v, want the empty-chain error", err)
	}
	if blocked.called.Load() {
		t.Fatal("IMDS was contacted despite AWS_EC2_METADATA_DISABLED")
	}
}

func TestIMDSHangFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()

	p, _ := newProvider(t, map[string]string{"AWS_EC2_METADATA_SERVICE_ENDPOINT": srv.URL}, Options{})
	start := time.Now()
	if _, err := p.Credentials(context.Background()); err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %s, want a fast failure off EC2", elapsed)
	}
}

const webIdentityToken = "eyJhbGciOi.super-secret-oidc-token.signature"

func TestWebIdentity(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(webIdentityToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clk := newClock()
	expiry := clk.now().Add(time.Hour).UTC()
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		form = r.PostForm
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", ct)
		}
		fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleWithWebIdentityResult>
    <Credentials>
      <AccessKeyId>AKIDIRSA</AccessKeyId>
      <SecretAccessKey>secret-irsa</SecretAccessKey>
      <SessionToken>token-irsa</SessionToken>
      <Expiration>%s</Expiration>
    </Credentials>
  </AssumeRoleWithWebIdentityResult>
</AssumeRoleWithWebIdentityResponse>`, expiry.Format(time.RFC3339))
	}))
	defer srv.Close()

	p := New(Options{
		STSEndpoint: srv.URL,
		Getenv: func(k string) string {
			return map[string]string{
				"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile,
				"AWS_ROLE_ARN":                "arn:aws:iam::123456789012:role/caveman",
			}[k]
		},
		Now: clk.now,
	})
	got, err := p.Credentials(context.Background())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if got.AccessKeyID != "AKIDIRSA" || got.SecretAccessKey != "secret-irsa" || got.SessionToken != "token-irsa" {
		t.Fatalf("unexpected credentials: %+v", got)
	}
	if p.Source() != "web_identity" {
		t.Fatalf("Source = %q, want web_identity", p.Source())
	}
	if form.Get("WebIdentityToken") != webIdentityToken {
		t.Fatalf("WebIdentityToken = %q, want the trimmed file contents", form.Get("WebIdentityToken"))
	}
	if form.Get("Action") != "AssumeRoleWithWebIdentity" || form.Get("Version") != "2011-06-15" {
		t.Fatalf("unexpected form: %v", form)
	}
	if !strings.HasPrefix(form.Get("RoleSessionName"), "caveman-proxy-") {
		t.Fatalf("RoleSessionName = %q", form.Get("RoleSessionName"))
	}
	if !p.expires.Equal(expiry) {
		t.Fatalf("expiry = %s, want %s", p.expires, expiry)
	}
}

func TestWebIdentityErrorOmitsToken(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(webIdentityToken), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		// A real STS error body echoes the request, token included.
		fmt.Fprintf(w, `<ErrorResponse><Error><Code>AccessDenied</Code>
			<Message>Not authorized to perform sts:AssumeRoleWithWebIdentity for %s</Message>
			</Error></ErrorResponse>`, webIdentityToken)
	}))
	defer srv.Close()

	p := New(Options{
		STSEndpoint: srv.URL,
		Getenv: func(k string) string {
			return map[string]string{
				"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile,
				"AWS_ROLE_ARN":                "arn:aws:iam::123456789012:role/caveman",
			}[k]
		},
	})
	_, err := p.Credentials(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "AccessDenied") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %v, want the status and the STS code", err)
	}
	if strings.Contains(err.Error(), webIdentityToken) {
		t.Fatalf("error leaked the web identity token: %v", err)
	}
}

// containerProvider serves credentials that expire an hour after each fetch and
// counts fetches.
func containerProvider(t *testing.T) (*Provider, *clock, *atomic.Int32, func()) {
	t.Helper()
	clk := newClock()
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		fmt.Fprint(w, credJSON(clk.now().Add(time.Hour)))
	}))
	p := New(Options{
		Getenv: func(k string) string {
			return map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL + "/creds"}[k]
		},
		Now: clk.now,
	})
	return p, clk, &fetches, srv.Close
}

func TestCacheRefreshesInsideExpiryWindow(t *testing.T) {
	p, clk, fetches, done := containerProvider(t)
	defer done()
	ctx := context.Background()

	if _, err := p.Credentials(ctx); err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	clk.advance(50 * time.Minute) // 10m of life left: still outside the window
	if _, err := p.Credentials(ctx); err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want the cached credentials reused", n)
	}
	clk.advance(6 * time.Minute) // 4m left: inside the 5m refresh window
	if _, err := p.Credentials(ctx); err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want a refresh near expiry", n)
	}
}

func TestNegativeCache(t *testing.T) {
	clk := newClock()
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := New(Options{
		Getenv: func(k string) string {
			return map[string]string{
				"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL + "/creds",
				"AWS_EC2_METADATA_DISABLED":          "true",
			}[k]
		},
		Now: clk.now,
	})
	ctx := context.Background()
	if _, err := p.Credentials(ctx); err == nil {
		t.Fatal("expected an error")
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want the failure cached", n)
	}
	clk.advance(9 * time.Second)
	if _, err := p.Credentials(ctx); err == nil {
		t.Fatal("expected the cached error")
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want no refetch inside the negative TTL", n)
	}
	clk.advance(2 * time.Second)
	if _, err := p.Credentials(ctx); err == nil {
		t.Fatal("expected an error")
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want a retry after the negative TTL", n)
	}
}

func TestConcurrentCallsShareOneFetch(t *testing.T) {
	clk := newClock()
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, credJSON(clk.now().Add(time.Hour)))
	}))
	defer srv.Close()

	p := New(Options{
		Getenv: func(k string) string {
			return map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL + "/creds"}[k]
		},
		Now: clk.now,
	})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := p.Credentials(context.Background())
			if err != nil {
				t.Errorf("Credentials: %v", err)
				return
			}
			if got.AccessKeyID != "AKIDCONTAINER" {
				t.Errorf("access key = %q", got.AccessKeyID)
			}
		}()
	}
	wg.Wait()
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want exactly one", n)
	}
}

func TestRegionFallback(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    Options
		environ map[string]string
		want    string
	}{
		{"explicit", Options{Region: "eu-west-1"}, map[string]string{"AWS_REGION": "us-west-2"}, "eu-west-1"},
		{"AWS_REGION", Options{}, map[string]string{"AWS_REGION": "us-west-2"}, "us-west-2"},
		{"AWS_DEFAULT_REGION", Options{}, map[string]string{"AWS_DEFAULT_REGION": "ap-south-1"}, "ap-south-1"},
		{"default", Options{}, nil, "us-east-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newProvider(t, tc.environ, tc.opts)
			if p.Region() != tc.want {
				t.Fatalf("Region = %q, want %q", p.Region(), tc.want)
			}
			if want := "https://sts." + tc.want + ".amazonaws.com"; p.stsEndpoint != want {
				t.Fatalf("stsEndpoint = %q, want %q", p.stsEndpoint, want)
			}
		})
	}
}

// outageProvider is containerProvider whose endpoint can be flipped to 500.
func outageProvider(t *testing.T) (*Provider, *clock, *atomic.Int32, *atomic.Bool, func()) {
	t.Helper()
	clk := newClock()
	var fetches atomic.Int32
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if down.Load() {
			http.Error(w, "metadata outage", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, credJSON(clk.now().Add(time.Hour)))
	}))
	p := New(Options{
		Getenv: func(k string) string {
			return map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL + "/creds"}[k]
		},
		Now: clk.now,
	})
	return p, clk, &fetches, &down, srv.Close
}

func TestStaleButValidCredentialsServedDuringOutage(t *testing.T) {
	p, clk, fetches, down, done := outageProvider(t)
	defer done()
	ctx := context.Background()
	if _, err := p.Credentials(ctx); err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	down.Store(true)
	clk.advance(56 * time.Minute) // inside the refresh window, 4m of life left
	got, err := p.Credentials(ctx)
	if err != nil || got.AccessKeyID != "AKIDCONTAINER" {
		t.Fatalf("got %+v err %v, want the stale-but-valid credentials", got, err)
	}
	// The failed refresh arms a retry floor: the next call inside negativeTTL
	// must not dial the endpoint again while still returning the credentials.
	clk.advance(2 * time.Second)
	if got, err := p.Credentials(ctx); err != nil || got.AccessKeyID != "AKIDCONTAINER" {
		t.Fatalf("got %+v err %v during retry floor", got, err)
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want one refresh attempt then a retry floor", n)
	}
	clk.advance(negativeTTL) // floor elapsed: retry, still down, still served
	if got, err := p.Credentials(ctx); err != nil || got.AccessKeyID != "AKIDCONTAINER" {
		t.Fatalf("got %+v err %v after retry floor", got, err)
	}
	if n := fetches.Load(); n != 3 {
		t.Fatalf("fetches = %d, want a retry after the floor", n)
	}
	clk.advance(5 * time.Minute) // now actually expired: the failure surfaces
	if _, err := p.Credentials(ctx); err == nil {
		t.Fatal("expired credentials were served after the outage outlived them")
	}
}

func TestCancelledCallerIsNotNegativeCached(t *testing.T) {
	clk := newClock()
	var fetches atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if fetches.Load() == 1 {
			<-release // hold the first fetch until its caller gives up
		}
		fmt.Fprint(w, credJSON(clk.now().Add(time.Hour)))
	}))
	defer srv.Close()
	p := New(Options{
		Getenv: func(k string) string {
			return map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL + "/creds"}[k]
		},
		Now: clk.now,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for fetches.Load() == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
		close(release)
	}()
	if _, err := p.Credentials(ctx); err == nil {
		t.Fatal("cancelled fetch returned credentials")
	}
	// Same instant, another caller: a hung-up client must not have poisoned
	// the cache for everyone else on the process.
	got, err := p.Credentials(context.Background())
	if err != nil || got.AccessKeyID != "AKIDCONTAINER" {
		t.Fatalf("got %+v err %v, want credentials after a caller cancellation", got, err)
	}
	// The walk is shared by everyone queued on p.mu, so the caller that started
	// it hanging up must not abandon it; it completed and its answer was cached.
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want the shared walk to survive its caller", n)
	}
}

func TestTemporaryCredentialWithoutExpiryStillRefreshes(t *testing.T) {
	clk := newClock()
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		fmt.Fprint(w, `{"AccessKeyId":"AKIDTEMP","SecretAccessKey":"secret","Token":"session"}`)
	}))
	defer srv.Close()
	p := New(Options{
		Getenv: func(k string) string {
			return map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL + "/creds"}[k]
		},
		Now: clk.now,
	})
	ctx := context.Background()
	if _, err := p.Credentials(ctx); err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	clk.advance(5 * time.Minute)
	if _, err := p.Credentials(ctx); err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want the synthetic 15m expiry honoured before its window", n)
	}
	clk.advance(6 * time.Minute) // 4m of the synthetic 15m left: refresh
	if _, err := p.Credentials(ctx); err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want a session-token credential refreshed despite no Expiration", n)
	}
}

// The whole chain runs under p.mu on the request path, so an endpoint that
// accepts a connection and then never answers used to hold every other Bedrock
// request behind it for the sum of the source timeouts. fetchTimeout caps one
// walk at three seconds.
func TestStalledEndpointBoundsCredentialsLatency(t *testing.T) {
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain first: with an unread request body the server cannot notice the
		// client hanging up, and httptest.Server.Close would wait out the sleep.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer stalled.Close()

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(webIdentityToken), 0o600); err != nil {
		t.Fatal(err)
	}
	// Web identity uses the STS client, whose own timeout is 10s — far past the
	// bound this test asserts.
	p, _ := newProvider(t, map[string]string{
		"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile,
		"AWS_ROLE_ARN":                "arn:aws:iam::123456789012:role/caveman-proxy",
	}, Options{STSEndpoint: stalled.URL})

	start := time.Now()
	if _, err := p.Credentials(context.Background()); err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("Credentials took %s behind a stalled endpoint, want the fetch bound to ~3s", elapsed)
	}
}
