// Package standalone wires the byte-safe gateway lifecycle into single-operator,
// BYOK, zero-cloud-dependency mode. It supplies the three injected seams the
// lifecycle needs — a static Authenticator, a BYOK CredentialResolver, and the
// caller's TelemetrySink — plus the SSRF-guarded upstream HTTP client that is
// always on in standalone (not gated on CAVE_ENV=prod).
package standalone

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/azureopenai"
	"github.com/JuliusBrussee/caveman/proxy/providers/bedrock"
	"github.com/JuliusBrussee/caveman/proxy/providers/gemini"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/proxy/providers/openaicompat"
	"github.com/JuliusBrussee/caveman/proxy/providers/vertex"
	"github.com/JuliusBrussee/caveman/shared/platform/awscreds"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"github.com/JuliusBrussee/caveman/shared/platform/ssrf"
)

// Auth is the single-operator authenticator: it returns a static context built
// from caveman.yaml. There is still no multi-tenant key to validate — token is
// one shared secret (CAVEMAN_AUTH_TOKEN), and it exists only so an operator can
// bind past loopback (config.validateListen refuses that bind without it).
type Auth struct {
	rc    gateway.RequestContext
	token string
}

// errInboundTokenRejected is deliberately uniform: the gateway maps any non-nil
// error to 401 cave_unauthorized, and an error that told a missing token apart
// from a wrong one would be an oracle for the caller probing the port. The
// operator still sees that the gate fired — gateway.Server.rejectUnauthorized
// logs the path and remote host and counts cave_proxy_unauthorized_total — so
// the silence here costs no observability.
var errInboundTokenRejected = errors.New("inbound token rejected")

func (a Auth) Authenticate(ctx context.Context, r *http.Request) (gateway.RequestContext, error) {
	if a.token == "" {
		// Loopback single-operator mode, unchanged: accept everything.
		return a.rc, nil
	}
	// The token is OURS, not the provider's, so it must not leave this hop — and
	// it must not still be in the header set when Creds.Resolve and
	// ClassifyResolvedAuthMode read the request, or the operator's shared secret
	// gets classified (and forwarded) as a provider credential. x-cave-api-key is
	// a caveman header, so it always goes. Authorization goes ONLY when it
	// carried the token: a real provider bearer (Claude Pro/Max OAuth, the
	// /chatgpt/ ChatGPT login) arrives in that same header and the request dies
	// without it.
	accepted := false
	if presented := strings.TrimSpace(r.Header.Get("x-cave-api-key")); presented != "" {
		r.Header.Del("x-cave-api-key")
		accepted = tokenEqual(presented, a.token)
	}
	// Checked even when x-cave-api-key already matched: a client that hedges and
	// sends the token in both headers would otherwise leave it in Authorization,
	// which every adapter forwards.
	if scheme, value, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " "); ok &&
		strings.EqualFold(scheme, "Bearer") && tokenEqual(strings.TrimSpace(value), a.token) {
		r.Header.Del("Authorization")
		accepted = true
	}
	if accepted {
		return a.rc, nil
	}
	return gateway.RequestContext{}, errInboundTokenRejected
}

// tokenEqual compares a presented secret in constant time so the port cannot be
// used to recover the token one byte at a time.
func tokenEqual(presented, token string) bool {
	return subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1
}

// Creds preserves a real inbound provider credential first; otherwise it falls
// back to the operator's BYOK env key. A key taken from an inbound
// `Authorization: Bearer` keeps that scheme (Claude Pro/Max OAuth tokens only
// work as a Bearer, never as x-api-key). Placeholder bearer tokens are preserved
// here so the gateway's upstream-header fallback can replace only that narrow
// case and log it.
type Creds struct {
	cfg config.Config
	// bedrock is the AWS default credential chain (task role, pod identity,
	// IRSA, instance profile) consulted only after the env pair says nothing.
	// Nil (hand-built Creds in tests) means env-only, exactly as before.
	bedrock *awscreds.Provider
	// logger and sourceLogged disclose, once, which chain entry the proxy signs
	// as: "which AWS identity am I billed as" must be observable.
	logger       *slog.Logger
	sourceLogged *sync.Once
}

func (c Creds) Resolve(provider string, r *http.Request) providers.Credential {
	fallbackEnv := c.authFallbackEnv(provider, r)
	// SDKs use provider-specific API-key headers. Resolve the selected provider's
	// native header before the legacy x-api-key alias or any configured fallback,
	// otherwise a shared listener can replace the caller's principal with its own.
	// Never consult another provider's native header on this route.
	key := ""
	switch provider {
	case "gemini":
		var err error
		key, err = gemini.RequestAPIKey(r)
		if err != nil {
			return providers.Credential{Mode: "ephemeral_header"}
		}
		if key == "" {
			// x-api-key is not a Google header. It stays supported as the alias
			// this proxy has always accepted, but only where Google's own
			// spellings said nothing — never as a competing account.
			key = strings.TrimSpace(r.Header.Get("x-api-key"))
		}
		// Keep an explicit OAuth credential unless the caller also sent Google's
		// native key header. A key that arrived only in the URL (or through the
		// alias) does not displace the principal the caller named in Authorization;
		// the adapter retains the caller-supplied URL key in its native header.
		if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" &&
			!strings.EqualFold(auth, "Bearer no-key-required") &&
			strings.TrimSpace(r.Header.Get("x-goog-api-key")) == "" {
			key = ""
		}
	case "azure_openai":
		key = strings.TrimSpace(r.Header.Get("api-key"))
	case "vertex":
		var err error
		key, err = providers.GoogleRequestAPIKey(r)
		if err != nil {
			return providers.Credential{Mode: "ephemeral_header"}
		}
		if key != "" {
			// Vertex's default credential is OAuth. Mark an explicitly selected
			// Express API key so it keeps Google's native header instead.
			return providers.Credential{Mode: "ephemeral_header", Key: key, Scheme: "api_key"}
		}
	}
	if key == "" && provider != "gemini" {
		key = strings.TrimSpace(r.Header.Get("x-api-key"))
	}
	if key == "" && (provider == "gemini" || provider == "vertex") && providers.GoogleRequestCarriesQueryCredential(r) {
		// The caller authenticated in the URL with a token this proxy does not
		// resolve. Forward it as sent; adding the operator's key beside it would
		// bill this request to a principal the caller never chose.
		return providers.Credential{Mode: "ephemeral_header"}
	}
	if k := key; k != "" {
		credential := providers.Credential{Mode: "ephemeral_header", Key: k, AuthFallbackEnv: fallbackEnv}
		if provider == "bedrock" {
			credential.AuthKind = "bedrock_api_key"
		}
		return credential
	}
	if a := strings.TrimSpace(r.Header.Get("authorization")); a != "" {
		if provider == "bedrock" && !strings.EqualFold(strings.Fields(a)[0], "Bearer") {
			// SDK SigV4 signatures cover the original authority/path/body and
			// cannot survive a base-URL swap. Resolve IAM separately from bearer
			// fallback; the Bedrock adapter checks the caller's requested principal
			// and signs the actual upstream bytes, or rejects the request.
			credential := c.bedrockSigningCredential(r.Context())
			credential.Scheme = "sigv4"
			credential.AuthKind = "aws_access_keys"
			return credential
		}
		credential := providers.Credential{Mode: "ephemeral_header", Key: bearerKey(a), Scheme: "bearer", AuthFallbackEnv: fallbackEnv}
		if provider == "bedrock" {
			credential.AuthKind = "bedrock_api_key"
		}
		return credential
	}
	if provider == "openai_compatible" {
		if name := compatNameFromPath(r.URL.Path); name != "" {
			if key, ok := c.cfg.CompatCredential(name); ok {
				return providers.Credential{Mode: "ephemeral_header", Key: key, AuthFallbackEnv: fallbackEnv}
			}
		}
	}
	if credential := c.cfg.Credential(provider); credential.Key != "" || credential.AuthFallbackEnv != "" {
		return credential
	}
	if provider == "bedrock" {
		// No bearer key and no env pair: a proxy running inside AWS still has a
		// role. Config.Credential cannot ask for it (it is a pure env read), so
		// the chain lives here, after every explicit source has declined.
		return c.bedrockSigningCredential(r.Context())
	}
	return providers.Credential{Mode: "ephemeral_header"}
}

// authFallbackEnv resolves the credential policy for this exact route. Named
// compat upstreams carry their own configured env name (or an empty name for
// explicitly unauthenticated routes); all other providers use the fixed BYOK
// mapping from Config.Credential. It is intentionally separate from the key
// value so placeholder inbound auth can be replaced without falling back to an
// unrelated process-wide provider secret.
func (c Creds) authFallbackEnv(provider string, r *http.Request) string {
	if provider == "openai_compatible" {
		if name := compatNameFromPath(r.URL.Path); name != "" {
			if upstream, ok := c.cfg.CompatUpstreams()[name]; ok {
				return strings.TrimSpace(upstream.APIKeyEnv)
			}
		}
	}
	return c.cfg.Credential(provider).AuthFallbackEnv
}

func compatNameFromPath(path string) string {
	const prefix = "/compat/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	name, _, ok := strings.Cut(rest, "/")
	if !ok || name == "" {
		return ""
	}
	return name
}

func bearerKey(raw string) string {
	value := strings.TrimSpace(raw)
	if len(value) > len("Bearer ") && strings.EqualFold(value[:len("Bearer")], "Bearer") && value[len("Bearer")] == ' ' {
		return strings.TrimSpace(value[len("Bearer "):])
	}
	return strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
}

// Options tunes the assembled server. A nil HTTPClient yields the SSRF-guarded
// standalone client; tests inject a plain client to reach a loopback stub. A nil
// Compressor disables S4 modes (they fall back to a record-mode pass-through);
// the binary wires the engine-backed one when mode is compress or pixel.
type Options struct {
	HTTPClient *http.Client
	Middleware http.Handler
	// Logger receives gateway warnings (upstream failures, copy errors). Nil
	// silences them, which is how the serve path ran until #897.
	Logger     *slog.Logger
	Compressor gateway.Compressor
	// PrefixCache is the durable original→replacement map that keeps a compressed
	// message byte-stable on every later turn (see gateway.PrefixCache). The binary
	// passes the same local SQLite store it uses for spend; without one the
	// non-PAYG live-zone paths stay closed.
	PrefixCache gateway.PrefixCache
	// RecoveryViaMCP makes compress mode rely on the agent's own caveman_retrieve
	// MCP tool (over the shared CCR store) instead of the proxy's server-side loop,
	// which lets streaming requests be compressed. `caveman wrap` sets it (via the
	// CAVEMAN_RECOVERY=mcp env) once it has installed that tool for the agent.
	RecoveryViaMCP bool
	// ObserveEstimate runs record mode as an observe-only would-have-saved
	// measurement (byte-safe pass-through + no CCR writes). Pair it with the
	// estimate-only compressor from NewEstimateCompressor.
	ObserveEstimate bool
	// SessionMarkerKey validates and strips native session correlation before the
	// request reaches provider adapters.
	SessionMarkerKey []byte
	// SessionFallback is conservative removed-marker correlation. It must return
	// empty when more than one recent native session could own request.
	SessionFallback func(time.Time, string, string) (string, string)
}

// New assembles a standalone gateway server from a config and a telemetry sink.
func New(cfg config.Config, sink gateway.TelemetrySink, opts Options) *gateway.Server {
	client := opts.HTTPClient
	if client == nil {
		client = StandaloneHTTPClient(cfg, time.Duration(env.Int("CAVE_GATEWAY_UPSTREAM_TIMEOUT_MS", 0))*time.Millisecond)
	}
	return gateway.New(gateway.Config{
		Middleware:           opts.Middleware,
		Adapters:             buildAdapters(cfg),
		Auth:                 Auth{rc: gateway.RequestContext{Label: cfg.Label, RuntimeMode: cfg.Mode, Optimizers: cfg.Optimizers, ProviderBillingTiers: cfg.BillingTiers()}, token: cfg.AuthToken},
		Creds:                Creds{cfg: cfg, bedrock: awscreds.New(awscreds.Options{Region: cfg.BedrockRegion()}), logger: opts.Logger, sourceLogged: new(sync.Once)},
		Sink:                 sink,
		Compressor:           opts.Compressor,
		PrefixCache:          opts.PrefixCache,
		RecoveryViaMCP:       opts.RecoveryViaMCP || env.String("CAVEMAN_RECOVERY", "") == "mcp",
		ObserveEstimate:      opts.ObserveEstimate || cfg.ObserveEstimate,
		SubscriptionCompress: cfg.SubscriptionCompress,
		SessionMarkerKey:     opts.SessionMarkerKey,
		SessionFallback:      opts.SessionFallback,
		ToolSchemaStrip:      cfg.ToolSchemaStrip,
		BreakpointPlan:       cfg.BreakpointPlan,
		HTTPClient:           client,
		Logger:               opts.Logger,
	})
}

// engineCompressor is the engine-backed gateway.Compressor: it compresses each
// extracted content segment through the Caveman engine and stores each original
// live-zone block in the same CCR store so the disclosed in-block handle is
// retrievable via engine.Retrieve. Pixel mode uses only the storage half for its
// full-request recovery record. Everything it produces is `inferred`.
type engineCompressor struct {
	eng   *engine.Engine
	store *ccr.Store
}

// NewEngineCompressor builds the engine-backed compressor over a CCR store. The
// engine shares that store, so a handle returned by StoreOriginal resolves through
// engine.Retrieve to the exact original bytes supplied by the gateway.
func NewEngineCompressor(store *ccr.Store) gateway.Compressor {
	return &engineCompressor{eng: engine.New(store, nil), store: store}
}

// NewEstimateCompressor builds the observe-only compressor for record-mode
// estimation. It has NO CCR store: it only ever runs the engine's network-free
// Simulate path (via EstimateSegment) to measure the token reduction compression
// would achieve, and it never stores a recovery original. StoreOriginal fails
// closed with no store, and CompressSegment falls back to pass-through — but the
// observe path calls neither; it uses EstimateSegment alone.
func NewEstimateCompressor() gateway.Compressor {
	return &engineCompressor{eng: engine.New(nil, nil), store: nil}
}

func (c *engineCompressor) CompressSegment(segment []byte) ([]byte, int, int) {
	res, err := c.eng.Compress(segment, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		return segment, 0, 0 // byte-safe: keep the original segment, claim no delta.
	}
	return res.Output, res.TokensBefore, res.TokensAfter
}

func (c *engineCompressor) CompressSegmentType(segment []byte, contentType string) ([]byte, int, int) {
	res, err := c.eng.Compress(segment, engine.Options{Mode: engine.ModeCompress, Type: contentType})
	if err != nil {
		return segment, 0, 0
	}
	return res.Output, res.TokensBefore, res.TokensAfter
}

// CompressSegmentQuery runs the same engine path with latest-user relevance
// context. Only query-aware compressors consume it; every other content type keeps
// byte-for-byte historical behavior.
func (c *engineCompressor) CompressSegmentQuery(segment []byte, query string) ([]byte, int, int) {
	res, err := c.eng.Compress(segment, engine.Options{Mode: engine.ModeCompress, Query: query})
	if err != nil {
		return segment, 0, 0
	}
	return res.Output, res.TokensBefore, res.TokensAfter
}

// EstimateSegment measures the token reduction a real CompressSegment would achieve
// through the engine's Simulate path, which stores NOTHING (no CCR Put) — so the
// observe-estimate record path is provably non-mutating and writes no recovery row.
func (c *engineCompressor) EstimateSegment(segment []byte) (int, int) {
	sim := c.eng.Simulate(segment, engine.Options{Mode: engine.ModeCompress})
	return sim.TokensBefore, sim.TokensAfter
}

// EstimateSegmentQuery mirrors CompressSegmentQuery without storing recovery data.
func (c *engineCompressor) EstimateSegmentQuery(segment []byte, query string) (int, int) {
	sim := c.eng.Simulate(segment, engine.Options{Mode: engine.ModeCompress, Query: query})
	return sim.TokensBefore, sim.TokensAfter
}

// StripToolSchema removes the documentation-only JSON-Schema annotation keywords
// from a serialized tool catalog through the engine's canonical strip — the same
// exported pure function the replay bench prices, so the proxy and the bench can
// never disagree about what a stripped catalog looks like. It stores nothing; the
// gateway records the original through StoreOriginal.
func (c *engineCompressor) StripToolSchema(tools []byte) ([]byte, bool) {
	return compressors.StripToolSchemaAnnotations(tools)
}

func (c *engineCompressor) StoreOriginal(body []byte) (string, error) {
	if c.store == nil {
		// The estimate-only compressor has no store: fail closed so a caller that
		// mistakenly tries to store a recovery original gets a byte-safe pass-through
		// (empty handle) instead of a panic. Compress/pixel modes always have a store.
		return "", ccr.ErrNotFound
	}
	return c.store.Put(ccr.Recovery{ContentType: "block", Compressor: "proxy-content", Original: body})
}

// RetrieveOriginal recovers the content behind a CCR handle. With no query it
// returns the byte-exact original block; with a query it returns only the elided
// sections most relevant to it (so a model that needs one detail does not re-ingest
// the whole prompt). The BM25 narrowing lives in the engine (engine.RetrieveQuery)
// so the proxy and the MCP server share one recovery implementation.
func (c *engineCompressor) RetrieveOriginal(handle, query string) ([]byte, error) {
	return c.eng.RetrieveQuery(handle, query)
}

// buildAdapters makes the provider adapters. Anthropic, OpenAI, and Gemini
// always get their public default upstream, so a bare `caveman start` works.
// Bedrock gets the Runtime endpoint of the resolved AWS region. An operator gives
// a raw Bedrock URL only for a custom endpoint. Azure, Vertex, and the legacy
// OpenAI-compatible adapter stay opt-in because they have no universal endpoint.
// The named compat mounts come from Config.CompatUpstreams, which includes the
// built-in OpenCode Go mount.
// ProviderUpstreams publishes the same native base URLs that buildAdapters uses.
// Wrappers must match a selected host's endpoint against the running proxy, not
// a configuration file that may have changed after this listener started.
func ProviderUpstreams(cfg config.Config) map[string]string {
	return map[string]string{
		"anthropic": cfg.BaseURL("anthropic", "https://api.anthropic.com"),
		"openai":    cfg.BaseURL("openai", "https://api.openai.com"),
		"gemini":    cfg.BaseURL("gemini", "https://generativelanguage.googleapis.com"),
	}
}

func buildAdapters(cfg config.Config) []providers.Adapter {
	upstreams := ProviderUpstreams(cfg)
	adapters := []providers.Adapter{
		anthropic.New(upstreams["anthropic"]),
		openai.New(upstreams["openai"]),
		gemini.New(upstreams["gemini"]),
		bedrock.New(cfg.BedrockBaseURL()),
	}
	if u := cfg.BaseURL("azure_openai", ""); u != "" {
		adapters = append(adapters, azureopenai.New(u))
	}
	if u := cfg.BaseURL("vertex", ""); u != "" {
		adapters = append(adapters, vertex.New(u))
	}
	compat := cfg.CompatUpstreams()
	compatNames := make([]string, 0, len(compat))
	for name := range compat {
		compatNames = append(compatNames, name)
	}
	sort.Strings(compatNames)
	for _, name := range compatNames {
		adapter, err := openaicompat.NewNamedWithWireDialect(name, compat[name].BaseURL, compat[name].WireDialect, compat[name].ForwardHeaders...)
		if err != nil {
			// This error cannot occur through config.Load, which validates every
			// compat entry with the same ValidateName, ValidateBaseURL, and
			// ValidateWireDialect. The config package tests validate every
			// built-in entry. A caller that makes a Config by hand must give
			// Load-validated compat entries.
			panic(err)
		}
		adapters = append(adapters, adapter)
	}
	if u := cfg.BaseURL("openai_compatible", ""); u != "" {
		adapters = append(adapters, openaicompat.New(u))
	}
	return adapters
}

// StandaloneHTTPClient returns the upstream client used in standalone mode. The
// SSRF dial guard is ALWAYS on here (unlike the managed gateway, which only
// guards in prod): it blocks loopback/private/link-local/metadata addresses at
// dial time so a malicious or misconfigured request can't make the local proxy
// reach an internal host. Operators targeting a local model server (Ollama, a
// LAN inference box) set CAVE_SSRF_ALLOWLIST to opt specific hosts back in —
// which requires self-hosted mode: managed mode ignores the allowlist by
// contract, so building on ManagedConfig here would make the documented escape
// hatch a silent no-op (loopback/private stay blocked unless allowlisted).
// The upstream proxy selector and the extra TLS roots come from cfg (a Config
// that never went through config.Load is direct, on Go's default verification).
func StandaloneHTTPClient(cfg config.Config, timeout time.Duration) *http.Client {
	guard := ssrf.SelfHostedConfig()
	guard.Proxy = cfg.UpstreamProxyFunc()
	if raw := env.String("CAVE_SSRF_ALLOWLIST", ""); raw != "" {
		guard.AllowList = strings.Split(raw, ",")
	}
	client := ssrf.NewHTTPClient(guard)
	// Go otherwise injects Accept-Encoding: gzip when callers omit it and then
	// transparently decodes the provider response. Standalone record mode promises
	// exact response wire bytes, so transport compression must stay disabled.
	if transport, ok := client.Transport.(*http.Transport); ok {
		transport.DisableCompression = true
		gateway.BoundUpstreamTransport(transport)
		if rootCAs := cfg.RootCAs(); rootCAs != nil {
			transport.TLSClientConfig = &tls.Config{RootCAs: rootCAs, MinVersion: tls.VersionTLS12}
		}
	}
	client.Timeout = timeout
	return client
}
