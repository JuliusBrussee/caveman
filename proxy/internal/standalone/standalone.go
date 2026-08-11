// Package standalone wires the byte-safe gateway lifecycle into single-operator,
// BYOK, zero-cloud-dependency mode. It supplies the three injected seams the
// lifecycle needs — a static Authenticator, a BYOK CredentialResolver, and the
// caller's TelemetrySink — plus the SSRF-guarded upstream HTTP client that is
// always on in standalone (not gated on CAVE_ENV=prod).
package standalone

import (
	"context"
	"net/http"
	"sort"
	"strings"
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
	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"github.com/JuliusBrussee/caveman/shared/platform/ssrf"
)

// Auth is the single-operator authenticator: it accepts every request and
// returns a static context built from caveman.yaml. There is no multi-tenant key
// to validate — the proxy listens on loopback for one operator.
type Auth struct{ rc gateway.RequestContext }

func (a Auth) Authenticate(ctx context.Context, r *http.Request) (gateway.RequestContext, error) {
	return a.rc, nil
}

// Creds preserves a real inbound provider credential first; otherwise it falls
// back to the operator's BYOK env key. A key taken from an inbound
// `Authorization: Bearer` keeps that scheme (Claude Pro/Max OAuth tokens only
// work as a Bearer, never as x-api-key). Placeholder bearer tokens are preserved
// here so the gateway's upstream-header fallback can replace only that narrow
// case and log it.
type Creds struct{ cfg config.Config }

func (c Creds) Resolve(provider string, r *http.Request) providers.Credential {
	fallbackEnv := c.authFallbackEnv(provider, r)
	if k := strings.TrimSpace(r.Header.Get("x-api-key")); k != "" {
		credential := providers.Credential{Mode: "ephemeral_header", Key: k, AuthFallbackEnv: fallbackEnv}
		if provider == "bedrock" {
			credential.AuthKind = "bedrock_api_key"
		}
		return credential
	}
	if a := r.Header.Get("authorization"); a != "" {
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
			if upstream, ok := c.cfg.Compat[name]; ok {
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
		client = StandaloneHTTPClient(time.Duration(env.Int("CAVE_GATEWAY_UPSTREAM_TIMEOUT_MS", 900000)) * time.Millisecond)
	}
	return gateway.New(gateway.Config{
		Adapters:             buildAdapters(cfg),
		Auth:                 Auth{rc: gateway.RequestContext{Label: cfg.Label, RuntimeMode: cfg.Mode, Optimizers: cfg.Optimizers, ProviderBillingTiers: cfg.BillingTiers()}},
		Creds:                Creds{cfg: cfg},
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

// buildAdapters constructs the provider adapters. Anthropic, OpenAI, and Gemini
// always get sensible public defaults so a bare `caveman start` works. Bedrock's
// standard Runtime endpoint is derived from the resolved AWS region; operators
// only need a raw URL for advanced/custom endpoints. Azure, Vertex, and
// OpenAI-compatible remain opt-in because they have no universal endpoint.
func buildAdapters(cfg config.Config) []providers.Adapter {
	adapters := []providers.Adapter{
		anthropic.New(cfg.BaseURL("anthropic", "https://api.anthropic.com")),
		openai.New(cfg.BaseURL("openai", "https://api.openai.com")),
		gemini.New(cfg.BaseURL("gemini", "https://generativelanguage.googleapis.com")),
		bedrock.New(cfg.BedrockBaseURL()),
	}
	if u := cfg.BaseURL("azure_openai", ""); u != "" {
		adapters = append(adapters, azureopenai.New(u))
	}
	if u := cfg.BaseURL("vertex", ""); u != "" {
		adapters = append(adapters, vertex.New(u))
	}
	compatNames := make([]string, 0, len(cfg.Compat))
	for name := range cfg.Compat {
		compatNames = append(compatNames, name)
	}
	sort.Strings(compatNames)
	for _, name := range compatNames {
		adapter, err := openaicompat.NewNamed(name, cfg.Compat[name].BaseURL)
		if err != nil {
			// Unreachable via config.Load, which pre-validates every compat entry
			// with the same ValidateName/ValidateBaseURL. Callers constructing a
			// Config by hand must pass Load-validated compat entries.
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
func StandaloneHTTPClient(timeout time.Duration) *http.Client {
	cfg := ssrf.SelfHostedConfig()
	if raw := env.String("CAVE_SSRF_ALLOWLIST", ""); raw != "" {
		cfg.AllowList = strings.Split(raw, ",")
	}
	client := ssrf.NewHTTPClient(cfg)
	// Go otherwise injects Accept-Encoding: gzip when callers omit it and then
	// transparently decodes the provider response. Standalone record mode promises
	// exact response wire bytes, so transport compression must stay disabled.
	if transport, ok := client.Transport.(*http.Transport); ok {
		transport.DisableCompression = true
	}
	client.Timeout = timeout
	return client
}
