// Package identity resolves who is calling the framework middleware routes and
// which namespaces they may use. Four sources, checked in this order:
//
//   - the legacy shared token (CAVEMAN_AUTH_TOKEN): principal single_operator,
//     every namespace, exactly as before;
//   - a token map file: sha256(token) → principal, namespace globs and quota
//     overrides, several tokens per principal so a token rotates without an
//     outage;
//   - an OIDC/JWT bearer verified against the issuer's JWKS;
//   - a TLS client certificate verified against the listener's client CA.
//
// With none configured the listener is loopback-only and every caller is
// single_operator ("open"), which is how the proxy always behaved there.
//
// A principal's name owns its data, so no source may mint another source's
// names. Token map principals are named as listed; JWT principals are
// oidc:<issuer>#<claim> and certificate principals mtls:uri:<URI SAN>,
// mtls:dns:<DNS SAN> or (opt-in) mtls:cn:<CN>, all compared byte for byte. A
// token map entry named with one of those prefixes supplies that principal's
// namespaces (when the JWT carries none) and quota, and may list no token. The
// token map may define single_operator, to move the legacy token's data behind
// rotatable tokens.
package identity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Operator is the legacy principal: the shared token, or no credential at all
// on an open loopback listener.
const Operator = "single_operator"

// Quota overrides a runtime-wide admission limit for one principal. Zero keeps
// the runtime-wide value.
type Quota struct {
	Rows              int64 `yaml:"rows"`
	Bytes             int64 `yaml:"bytes"`
	RequestsPerMinute int   `yaml:"requests_per_minute"`
}

// Principal is an authenticated caller. Its Name owns its data: every scope,
// grant and original is keyed by it.
type Principal struct {
	Name string
	// Mechanism is how the caller authenticated, for the audit log: open, token,
	// token_map, oidc or mtls.
	Mechanism  string
	Quota      Quota
	namespaces *regexp.Regexp
}

// NewPrincipal compiles namespace globs: "*" matches any run of characters,
// "/" included, and every other character matches itself. No globs allows no
// namespace; "*" allows every one.
func NewPrincipal(name, mechanism string, globs []string, quota Quota) (Principal, error) {
	if !validName(name) {
		return Principal{}, fmt.Errorf("principal %q: 1-1024 printable characters, no spaces, quotes or backslashes", name)
	}
	if quota.Rows < 0 || quota.Bytes < 0 || quota.RequestsPerMinute < 0 {
		return Principal{}, fmt.Errorf("principal %q: quota values must not be negative", name)
	}
	p := Principal{Name: name, Mechanism: mechanism, Quota: quota}
	if len(globs) == 0 {
		return p, nil
	}
	alternatives := make([]string, len(globs))
	for i, glob := range globs {
		if !globPattern.MatchString(glob) {
			return Principal{}, fmt.Errorf("principal %q: namespace glob %q must match %s", name, glob, globPattern)
		}
		alternatives[i] = strings.ReplaceAll(regexp.QuoteMeta(glob), `\*`, `.*`)
	}
	// RE2: matching is linear in the namespace whatever the globs are.
	p.namespaces = regexp.MustCompile(`^(?:` + strings.Join(alternatives, "|") + `)$`)
	return p, nil
}

// Allows reports whether the principal may use namespace.
func (p Principal) Allows(namespace string) bool {
	return p.namespaces != nil && p.namespaces.MatchString(namespace)
}

// HasNamespaces reports whether the principal may use any namespace at all.
func (p Principal) HasNamespaces() bool { return p.namespaces != nil }

func operator(mechanism string) Principal {
	p, _ := NewPrincipal(Operator, mechanism, []string{"*"}, Quota{})
	return p
}

// globPattern is the scope-token grammar plus "*".
var globPattern = regexp.MustCompile(`^[A-Za-z0-9._:/*-]{1,256}$`)

// validName bounds a principal name; 1024 leaves room for a source prefix and
// an issuer URL before a 256-character claim.
func validName(name string) bool {
	if name == "" || len(name) > 1024 {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c <= ' ' || c > '~' || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// Config selects the identity sources. Zero values leave a source off.
type Config struct {
	// Token is CAVEMAN_AUTH_TOKEN.
	Token        string
	TokenMapFile string
	OIDC         OIDC
	// MTLS accepts a client certificate the TLS listener verified; see
	// UseServerTLS.
	MTLS bool
	// MTLSCommonName names a certificate with neither a URI nor a DNS SAN by its
	// subject CN. Off by default: a CN is free text many CAs fill carelessly.
	MTLSCommonName bool
	// HTTPClient fetches the JWKS; nil uses a client with a 5 s timeout.
	HTTPClient *http.Client
	Logger     *slog.Logger
	Now        func() time.Time
}

// Resolver authenticates middleware requests. Safe for concurrent use;
// Reload swaps the token map atomically.
type Resolver struct {
	token     []byte
	file      string
	stamp     string
	tokens    atomic.Pointer[tokenMap]
	oidc      *oidcVerifier
	mtls      bool
	mtlsCN    bool
	serverTLS *ServerTLS
	logger    *slog.Logger
	now       func() time.Time
}

var errUnauthorized = errors.New("identity: unauthenticated")

func New(cfg Config) (*Resolver, error) {
	r := &Resolver{token: []byte(cfg.Token), file: cfg.TokenMapFile, mtls: cfg.MTLS, mtlsCN: cfg.MTLSCommonName, logger: cfg.Logger, now: cfg.Now}
	if r.now == nil {
		r.now = time.Now
	}
	r.tokens.Store(&tokenMap{})
	if r.file != "" {
		if _, err := r.Reload(true); err != nil {
			return nil, err
		}
	}
	if cfg.OIDC.Issuer != "" {
		v, err := newOIDCVerifier(cfg.OIDC, cfg.HTTPClient, cfg.Logger, r.now)
		if err != nil {
			return nil, err
		}
		r.oidc = v
	}
	return r, nil
}

// Open reports that no credential is configured: every caller is the operator.
func (r *Resolver) Open() bool {
	return len(r.token) == 0 && r.file == "" && r.oidc == nil && !r.mtls
}

// MultiPrincipal reports that callers can be principals other than the operator.
func (r *Resolver) MultiPrincipal() bool { return r.file != "" || r.oidc != nil || r.mtls }

// UseServerTLS re-verifies a client certificate against t's current client CA
// on every request, so a CA rotation also cuts keep-alive connections opened
// under the old one. Call it before serving; with MTLS and no ServerTLS, no
// certificate identifies anyone.
func (r *Resolver) UseServerTLS(t *ServerTLS) { r.serverTLS = t }

// Identify resolves the request's principal from its credential alone: a bearer
// (Authorization: Bearer, or x-cave-api-key for static tokens) decides when one
// is present, else a verified client certificate.
func (r *Resolver) Identify(req *http.Request) (Principal, error) {
	if r.Open() {
		return operator("open"), nil
	}
	bearer := ""
	if scheme, value, ok := strings.Cut(strings.TrimSpace(req.Header.Get("Authorization")), " "); ok && strings.EqualFold(scheme, "Bearer") {
		bearer = strings.TrimSpace(value)
	}
	apiKey := strings.TrimSpace(req.Header.Get("x-cave-api-key"))
	for _, presented := range []string{bearer, apiKey} {
		if presented == "" {
			continue
		}
		if len(r.token) > 0 && subtle.ConstantTimeCompare([]byte(presented), r.token) == 1 {
			return operator("token"), nil
		}
		if p, ok := r.tokens.Load().lookup(sha256.Sum256([]byte(presented))); ok {
			return p, nil
		}
	}
	if bearer != "" && r.oidc != nil && strings.Count(bearer, ".") == 2 {
		claims, err := r.oidc.verify(bearer)
		if err != nil {
			return Principal{}, errUnauthorized
		}
		return r.oidcPrincipal(claims)
	}
	if bearer != "" || apiKey != "" {
		return Principal{}, errUnauthorized
	}
	if r.mtls && req.TLS != nil && len(req.TLS.VerifiedChains) > 0 && len(req.TLS.PeerCertificates) > 0 {
		// The handshake checked the certificate against the client CA of its
		// moment; a keep-alive connection outlives a CA rotation.
		if r.serverTLS == nil || !r.serverTLS.verified(req.TLS) {
			return Principal{}, errUnauthorized
		}
		cert := req.TLS.PeerCertificates[0]
		switch {
		case len(cert.URIs) > 0:
			// Not cert.URIs[0].String(): url.Parse lowercases the scheme, which
			// would make SPIFFE://x and spiffe://x one principal.
			uri, _ := firstURISAN(cert)
			return r.mapped("mtls:uri:", uri, "mtls", nil)
		case len(cert.DNSNames) > 0:
			return r.mapped("mtls:dns:", cert.DNSNames[0], "mtls", nil)
		case r.mtlsCN:
			return r.mapped("mtls:cn:", cert.Subject.CommonName, "mtls", nil)
		}
	}
	return Principal{}, errUnauthorized
}

var oidSubjectAltName = asn1.ObjectIdentifier{2, 5, 29, 17}

// firstURISAN returns the certificate's first URI SAN exactly as issued.
func firstURISAN(cert *x509.Certificate) (string, bool) {
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(oidSubjectAltName) {
			continue
		}
		var names []asn1.RawValue
		if rest, err := asn1.Unmarshal(ext.Value, &names); err != nil || len(rest) > 0 {
			return "", false
		}
		for _, name := range names {
			// GeneralName uniformResourceIdentifier: [6] IMPLICIT IA5String,
			// primitive only. Go's parser, and so its name-constraint check,
			// skips a constructed [6].
			if name.Class == asn1.ClassContextSpecific && name.Tag == 6 && !name.IsCompound {
				// It must be the URI Go verified; if the two parsers disagree,
				// identify no one rather than guess.
				raw := string(name.Bytes)
				if u, err := url.Parse(raw); err != nil || len(cert.URIs) == 0 || u.String() != cert.URIs[0].String() {
					return "", false
				}
				return raw, true
			}
		}
	}
	return "", false
}

func (r *Resolver) oidcPrincipal(claims map[string]any) (Principal, error) {
	name, _ := claims[r.oidc.cfg.PrincipalClaim].(string)
	// A present claim decides, even when empty; only an absent one falls back
	// to the token map (globs stays nil).
	var globs []string
	if value, present := claims[r.oidc.cfg.NamespacesClaim]; present && r.oidc.cfg.NamespacesClaim != "" {
		globs = []string{}
		switch value := value.(type) {
		case string:
			globs = append(globs, strings.Fields(value)...)
		case []any:
			for _, item := range value {
				glob, ok := item.(string)
				if !ok {
					return Principal{}, errUnauthorized
				}
				globs = append(globs, glob)
			}
		default:
			return Principal{}, errUnauthorized
		}
	}
	return r.mapped("oidc:"+r.oidc.cfg.Issuer+"#", name, "oidc", globs)
}

// mapped builds the JWT or certificate principal prefix+value. globs from the
// credential win; otherwise the token map entry of that name supplies them.
// Quota always comes from that entry.
func (r *Resolver) mapped(prefix, value, mechanism string, globs []string) (Principal, error) {
	if value == Operator || !validName(value) {
		return Principal{}, errUnauthorized
	}
	name := prefix + value
	entry := r.tokens.Load().byName[name]
	if globs == nil {
		globs = entry.Namespaces
	}
	p, err := NewPrincipal(name, mechanism, globs, entry.Quota)
	if err != nil {
		return Principal{}, errUnauthorized
	}
	return p, nil
}

// tokenFile is the token map file (YAML or JSON). Only token hashes are stored:
// sha256 of the token, hex encoded, e.g.
// `printf %s "$TOKEN" | sha256sum`.
type tokenFile struct {
	Principals []tokenEntry `yaml:"principals"`
}

type tokenEntry struct {
	Name        string   `yaml:"name"`
	Namespaces  []string `yaml:"namespaces"`
	TokenSHA256 []string `yaml:"token_sha256"`
	Quota       Quota    `yaml:"quota"`
}

type tokenMap struct {
	hashes     [][32]byte
	principals []Principal // parallel to hashes
	byName     map[string]tokenEntry
}

// lookup compares against every hash without stopping early.
func (m *tokenMap) lookup(sum [32]byte) (Principal, bool) {
	found := -1
	for i := range m.hashes {
		if subtle.ConstantTimeCompare(m.hashes[i][:], sum[:]) == 1 {
			found = i
		}
	}
	if found < 0 {
		return Principal{}, false
	}
	return m.principals[found], true
}

func loadTokenMap(path string) (*tokenMap, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("token map: %w", err)
	}
	var file tokenFile
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("token map %s: %w", path, err)
	}
	m := &tokenMap{byName: map[string]tokenEntry{}}
	seen := map[[32]byte]bool{}
	for _, entry := range file.Principals {
		if _, dup := m.byName[entry.Name]; dup {
			return nil, fmt.Errorf("token map %s: principal %q listed twice", path, entry.Name)
		}
		// A token for an OIDC or certificate principal would let its holder
		// into that principal's sessions; a malformed prefix matches no one.
		if prefixed, ok := sourceName(entry.Name); prefixed && (!ok || len(entry.TokenSHA256) > 0) {
			return nil, fmt.Errorf("token map %s: principal %q: entries named oidc:<issuer>#<claim> or mtls:{uri,dns,cn}:<name> set namespaces and quota only, with no token_sha256", path, entry.Name)
		}
		p, err := NewPrincipal(entry.Name, "token_map", entry.Namespaces, entry.Quota)
		if err != nil {
			return nil, fmt.Errorf("token map %s: %w", path, err)
		}
		m.byName[entry.Name] = entry
		for _, hash := range entry.TokenSHA256 {
			b, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(hash)))
			if err != nil || len(b) != sha256.Size {
				// A hash, not a token, but still never echoed.
				return nil, fmt.Errorf("token map %s: principal %q has a token_sha256 entry that is not 64 hex characters", path, entry.Name)
			}
			sum := [32]byte(b)
			if seen[sum] {
				return nil, fmt.Errorf("token map %s: a token hash is listed twice", path)
			}
			seen[sum] = true
			m.hashes = append(m.hashes, sum)
			m.principals = append(m.principals, p)
		}
	}
	return m, nil
}

// sourceName reports whether name carries an OIDC or certificate source
// prefix, and whether it is well formed.
func sourceName(name string) (prefixed, ok bool) {
	if rest, found := strings.CutPrefix(name, "oidc:"); found {
		issuer, claim, found := strings.Cut(rest, "#")
		return true, found && issuer != "" && claim != ""
	}
	if rest, found := strings.CutPrefix(name, "mtls:"); found {
		kind, value, found := strings.Cut(rest, ":")
		return true, found && value != "" && (kind == "uri" || kind == "dns" || kind == "cn")
	}
	return false, true
}

// Reload re-reads the token map; unless force, only when the file changed. A
// file that no longer parses keeps the previous map and returns the error.
func (r *Resolver) Reload(force bool) (bool, error) {
	if r.file == "" {
		return false, nil
	}
	stamp := fileStamp(r.file)
	if !force && stamp == r.stamp {
		return false, nil
	}
	m, err := loadTokenMap(r.file)
	if err != nil {
		return false, err
	}
	for name, entry := range m.byName {
		// Before source prefixes, this is how an OIDC or certificate principal
		// was configured; it now configures nobody.
		if prefixed, _ := sourceName(name); !prefixed && len(entry.TokenSHA256) == 0 && r.logger != nil {
			r.logger.Warn("token map principal has no token_sha256 and matches no caller; name OIDC and certificate principals oidc:<issuer>#<claim> or mtls:{uri,dns,cn}:<name>", "principal", name)
		}
	}
	r.tokens.Store(m)
	r.stamp = stamp
	return true, nil
}

// fileStamp changes whenever any of the files is replaced, rewritten or
// removed, including a Kubernetes Secret volume's atomic symlink swap.
func fileStamp(paths ...string) string {
	var b strings.Builder
	for _, path := range paths {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(&b, "%s:%v;", path, err)
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d;", path, info.ModTime().UnixNano(), info.Size())
	}
	return b.String()
}

// Reloader re-reads configuration from files; unless force, only on change.
type Reloader interface {
	Reload(force bool) (bool, error)
}

// Watch reloads every reloader when its files change (checked every interval)
// and unconditionally on SIGHUP, until ctx ends. A failed reload keeps the
// previous configuration, revoked tokens included, so it is counted for
// WriteMetrics as well as logged.
func Watch(ctx context.Context, logger *slog.Logger, every time.Duration, reloaders map[string]Reloader) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	tick := time.NewTicker(every)
	defer tick.Stop()
	for name := range reloaders {
		noteReload(name, false) // each loaded before Watch starts
	}
	for {
		force := false
		select {
		case <-ctx.Done():
			return
		case <-hup:
			force = true
		case <-tick.C:
		}
		for name, reloader := range reloaders {
			reloaded, err := reloader.Reload(force)
			if err != nil {
				noteReload(name, true)
				logger.Error("reload failed; keeping the previous configuration", "source", name, "error", err)
			} else if reloaded {
				noteReload(name, false)
				logger.Info("reloaded", "source", name)
			}
		}
	}
}

// reloads backs the reload metrics; process-wide, like SIGHUP. Sources are
// Watch's map keys (token_map, tls), so labels stay bounded.
var reloads struct {
	sync.Mutex
	failures    map[string]int64
	lastSuccess map[string]time.Time
}

func noteReload(source string, failed bool) {
	reloads.Lock()
	defer reloads.Unlock()
	if reloads.failures == nil {
		reloads.failures, reloads.lastSuccess = map[string]int64{}, map[string]time.Time{}
	}
	if failed {
		reloads.failures[source]++
		return
	}
	reloads.failures[source] += 0 // the series exists from the first load
	reloads.lastSuccess[source] = time.Now()
}

// WriteMetrics appends the reload series in Prometheus text format; nothing
// before Watch starts.
func WriteMetrics(w io.Writer) {
	reloads.Lock()
	defer reloads.Unlock()
	if len(reloads.failures) == 0 {
		return
	}
	sources := slices.Sorted(maps.Keys(reloads.failures))
	var b strings.Builder
	b.WriteString("# HELP caveman_identity_reload_failures_total Token map or TLS file reloads that failed; the previous configuration, revoked tokens included, stays in force.\n" +
		"# TYPE caveman_identity_reload_failures_total counter\n")
	for _, source := range sources {
		fmt.Fprintf(&b, "caveman_identity_reload_failures_total{source=%q} %d\n", source, reloads.failures[source])
	}
	b.WriteString("# HELP caveman_identity_reload_last_success_timestamp_seconds When the configuration in force was loaded, in Unix seconds.\n" +
		"# TYPE caveman_identity_reload_last_success_timestamp_seconds gauge\n")
	for _, source := range sources {
		fmt.Fprintf(&b, "caveman_identity_reload_last_success_timestamp_seconds{source=%q} %d\n", source, reloads.lastSuccess[source].Unix())
	}
	_, _ = io.WriteString(w, b.String())
}
