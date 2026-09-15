// Package githubapp is the single home for the Cave Agent's GitHub App identity:
// it turns the App private key into a short-lived App JWT, mints JIT per-install
// installation tokens scoped to ONE repo with least privilege
// (contents:write + pull_requests:write — never merge), and revokes them at
// job end. Both control-api (select-repo verification) and the worker (the PR
// opener) import it.
//
// Callers resolve App private material through the shared KMS envelope loader in
// production before constructing Config. Installation tokens remain JIT-minted,
// ~1h, revoked at job end, and NEVER persisted.
//
// All HTTP egress flows through ssrf.NewHTTPClient(ssrf.ManagedConfig()); the base
// host is fixed api.github.com, and a configurable GitHub Enterprise base_url is
// run through ssrf.ValidateURL before use.
package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/ssrf"
)

const defaultBaseURL = "https://api.github.com"

// Config configures the App. PrivateKeyPEM is the raw PEM bytes (PKCS#1 or
// PKCS#8 RSA). BaseURL defaults to api.github.com; a GHE override is SSRF-checked.
type Config struct {
	AppID         string
	AppSlug       string
	PrivateKeyPEM []byte
	WebhookSecret string
	BaseURL       string
	// HTTPClient overrides the SSRF-guarded client (tests inject an httptest one).
	HTTPClient *http.Client
}

// App holds the parsed App identity and the SSRF-guarded HTTP client.
type App struct {
	appID         string
	slug          string
	privateKey    *rsa.PrivateKey
	webhookSecret string
	baseURL       string
	httpClient    *http.Client
}

// New parses the private key, validates the base URL, and builds the App. It
// returns an error (not a half-built App) on any misconfiguration, so callers
// fail closed — an unconfigured deployment leaves the App nil and the connect
// endpoints answer a clean "disabled" rather than a fabricated success.
func New(cfg Config) (*App, error) {
	if strings.TrimSpace(cfg.AppID) == "" {
		return nil, fmt.Errorf("githubapp: app id is required")
	}
	if len(cfg.PrivateKeyPEM) == 0 {
		return nil, fmt.Errorf("githubapp: private key PEM is required")
	}
	key, err := parseRSAPrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	client := cfg.HTTPClient
	if client == nil {
		// Production path: SSRF-guarded client + pre-flight host check on a custom
		// (GHE) base. When a caller injects a client (tests), it owns the host policy,
		// so we skip the pre-flight — but production never injects one.
		if base != defaultBaseURL {
			if err := ssrf.ValidateURL(context.Background(), base, ssrf.ManagedConfig()); err != nil {
				return nil, fmt.Errorf("githubapp: base_url rejected by SSRF guard: %w", err)
			}
		}
		client = ssrf.NewHTTPClient(ssrf.ManagedConfig())
		client.Timeout = 20 * time.Second
	}
	return &App{
		appID:         strings.TrimSpace(cfg.AppID),
		slug:          strings.TrimSpace(cfg.AppSlug),
		privateKey:    key,
		webhookSecret: cfg.WebhookSecret,
		baseURL:       base,
		httpClient:    client,
	}, nil
}

// Slug returns the App slug used to build the install URL.
func (a *App) Slug() string { return a.slug }

// WebhookSecret returns the configured webhook HMAC secret (for signature verify).
func (a *App) WebhookSecret() string { return a.webhookSecret }

// AppJWT builds a short-lived (10-minute) RS256 App JWT signed with the App
// private key. iat is backdated 60s to tolerate clock skew (GitHub's guidance).
func (a *App) AppJWT() (string, error) {
	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": a.appID,
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("githubapp: sign app jwt: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// InstallationToken is the JIT, least-agency credential. It is scoped to the
// single repo named and to the supplied permissions only, and is NEVER
// persisted. The caller revokes it via RevokeToken at job end.
type InstallationToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Installation is the App-authenticated identity GitHub assigns to an install.
// Callers use it to reject callback-supplied ids that do not belong to this App.
type Installation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	} `json:"account"`
}

// GetInstallation verifies an installation id against GitHub using the App JWT.
func (a *App) GetInstallation(ctx context.Context, installationID int64) (Installation, error) {
	var out Installation
	if installationID <= 0 {
		return out, fmt.Errorf("githubapp: installation id must be positive")
	}
	jwt, err := a.AppJWT()
	if err != nil {
		return out, err
	}
	status, raw, err := a.do(ctx, "Bearer "+jwt, http.MethodGet,
		"/app/installations/"+strconv.FormatInt(installationID, 10), nil)
	if err != nil {
		return out, err
	}
	if status != http.StatusOK {
		return out, fmt.Errorf("githubapp: get installation: HTTP %d: %s", status, snippet(raw))
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("githubapp: decode installation: %w", err)
	}
	if out.ID != installationID || out.Account.ID <= 0 || strings.TrimSpace(out.Account.Login) == "" {
		return Installation{}, fmt.Errorf("githubapp: installation response identity mismatch")
	}
	return out, nil
}

// MintInstallationToken POSTs /app/installations/{id}/access_tokens narrowed to
// `repos` and `perms`, returning a ~1h token. Defaults (perms nil) are
// contents:write + pull_requests:write — enough to push a branch and open a draft
// PR, never to merge.
func (a *App) MintInstallationToken(ctx context.Context, installationID int64, repos []string, perms map[string]string) (InstallationToken, error) {
	if installationID <= 0 {
		return InstallationToken{}, fmt.Errorf("githubapp: installation id must be positive")
	}
	if len(repos) != 1 || strings.TrimSpace(repos[0]) == "" {
		return InstallationToken{}, fmt.Errorf("githubapp: exactly one repository is required")
	}
	repository := strings.TrimSpace(repos[0])
	if perms == nil {
		perms = map[string]string{"contents": "write", "pull_requests": "write"}
	}
	allowedPermissions := map[string]bool{"contents": true, "pull_requests": true}
	scopedPermissions := make(map[string]string, len(perms))
	for name, level := range perms {
		if !allowedPermissions[name] || (level != "read" && level != "write") {
			return InstallationToken{}, fmt.Errorf("githubapp: permission %q=%q exceeds the least-agency allowlist", name, level)
		}
		scopedPermissions[name] = level
	}
	jwt, err := a.AppJWT()
	if err != nil {
		return InstallationToken{}, err
	}
	body := map[string]any{"permissions": scopedPermissions, "repositories": []string{repository}}
	status, raw, err := a.do(ctx, "Bearer "+jwt, http.MethodPost,
		"/app/installations/"+strconv.FormatInt(installationID, 10)+"/access_tokens", body)
	if err != nil {
		return InstallationToken{}, err
	}
	if status != http.StatusCreated {
		return InstallationToken{}, fmt.Errorf("githubapp: mint token: HTTP %d: %s", status, snippet(raw))
	}
	var out InstallationToken
	if err := json.Unmarshal(raw, &out); err != nil {
		return InstallationToken{}, fmt.Errorf("githubapp: decode token response: %w", err)
	}
	if out.Token == "" {
		return InstallationToken{}, fmt.Errorf("githubapp: token response carried no token")
	}
	return out, nil
}

// RevokeToken DELETEs /installation/token authenticated with the token itself —
// the job-end "drop all agency" step. A best-effort revoke; the ~1h natural
// expiry is the backstop.
func (a *App) RevokeToken(ctx context.Context, token string) error {
	status, raw, err := a.do(ctx, "Bearer "+token, http.MethodDelete, "/installation/token", nil)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent {
		return fmt.Errorf("githubapp: revoke token: HTTP %d: %s", status, snippet(raw))
	}
	return nil
}

// Repo is the minimal repo metadata the connect flow stores.
type Repo struct {
	ID            int64  `json:"id"`
	NodeID        string `json:"node_id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

// GetRepo verifies that the installation token can see owner/name and returns the
// repo's stable node id + default branch. Used by select-repo to confirm the repo
// truly belongs to the installation before the binding goes 'active'.
func (a *App) GetRepo(ctx context.Context, token, owner, name string) (Repo, error) {
	status, raw, err := a.do(ctx, "Bearer "+token, http.MethodGet,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil)
	if err != nil {
		return Repo{}, err
	}
	if status != http.StatusOK {
		return Repo{}, fmt.Errorf("githubapp: get repo %s/%s: HTTP %d: %s", owner, name, status, snippet(raw))
	}
	var r Repo
	if err := json.Unmarshal(raw, &r); err != nil {
		return Repo{}, fmt.Errorf("githubapp: decode repo: %w", err)
	}
	return r, nil
}

// GetRepoInstallation asks GitHub which installation of this App owns access to
// a repository. This avoids treating public repository visibility as proof that
// a caller-supplied installation id is authorized for that repository.
func (a *App) GetRepoInstallation(ctx context.Context, owner, name string) (Installation, error) {
	var out Installation
	jwt, err := a.AppJWT()
	if err != nil {
		return out, err
	}
	status, raw, err := a.do(ctx, "Bearer "+jwt, http.MethodGet,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/installation", nil)
	if err != nil {
		return out, err
	}
	if status != http.StatusOK {
		return out, fmt.Errorf("githubapp: get repo installation %s/%s: HTTP %d: %s", owner, name, status, snippet(raw))
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("githubapp: decode repo installation: %w", err)
	}
	if out.ID <= 0 {
		return Installation{}, fmt.Errorf("githubapp: repo installation response carried no id")
	}
	return out, nil
}

// GetFileContent reads one repository file with an installation token. The
// content endpoint returns base64; callers receive decoded bytes with a strict
// 64 KiB post-decode ceiling because connection proofs are tiny text files.
func (a *App) GetFileContent(ctx context.Context, token, owner, name, path, ref string) ([]byte, error) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return nil, fmt.Errorf("githubapp: content path is required")
	}
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	endpoint := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/contents/" + strings.Join(segments, "/")
	if strings.TrimSpace(ref) != "" {
		endpoint += "?" + url.Values{"ref": {ref}}.Encode()
	}
	status, raw, err := a.do(ctx, "Bearer "+token, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("githubapp: get repository proof: HTTP %d: %s", status, snippet(raw))
	}
	var payload struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Size     int64  `json:"size"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("githubapp: decode repository proof: %w", err)
	}
	if payload.Type != "file" || payload.Encoding != "base64" || payload.Size < 0 || payload.Size > 64<<10 {
		return nil, fmt.Errorf("githubapp: repository proof has invalid type, encoding, or size")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("githubapp: decode repository proof content: %w", err)
	}
	if len(decoded) > 64<<10 || int64(len(decoded)) != payload.Size {
		return nil, fmt.Errorf("githubapp: repository proof size mismatch")
	}
	return decoded, nil
}

// DoToken issues an authenticated GitHub REST call with an installation token and
// returns the status + raw body for the caller to parse. It is the reusable
// primitive the worker's PR opener builds the Git Data API flow on, so every
// GitHub egress goes through the one SSRF-guarded client + fixed base host.
func (a *App) DoToken(ctx context.Context, token, method, path string, body any) (int, []byte, error) {
	return a.do(ctx, "Bearer "+token, method, path, body)
}

func (a *App) do(ctx context.Context, authorization, method, path string, body any) (int, []byte, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "\\") {
		return 0, nil, fmt.Errorf("githubapp: request path must be a single-host absolute path")
	}
	base, err := url.Parse(a.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil {
		return 0, nil, fmt.Errorf("githubapp: invalid base URL")
	}
	relative, err := url.ParseRequestURI(path)
	if err != nil || relative.IsAbs() || relative.Host != "" || relative.User != nil {
		return 0, nil, fmt.Errorf("githubapp: invalid request path")
	}
	target, err := url.Parse(a.baseURL + path)
	if err != nil || target.Scheme != base.Scheme || !strings.EqualFold(target.Host, base.Host) || target.User != nil {
		return 0, nil, fmt.Errorf("githubapp: request path escaped configured host")
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("githubapp: marshal request: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return 0, nil, fmt.Errorf("githubapp: build request: %w", err)
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("githubapp: request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("githubapp: read response: %w", err)
	}
	return resp.StatusCode, raw, nil
}

// parseRSAPrivateKey accepts a PKCS#1 ("RSA PRIVATE KEY") or PKCS#8
// ("PRIVATE KEY") PEM — GitHub Apps download PKCS#1, but Cloud KMS / openssl
// conversions emit PKCS#8, so we accept both.
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("githubapp: private key is not valid PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("githubapp: private key is neither PKCS#1 nor PKCS#8 RSA: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("githubapp: private key is not RSA")
	}
	return key, nil
}

// snippet trims an error body so we never echo a large/secret-bearing response.
func snippet(b []byte) string {
	const max = 256
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
