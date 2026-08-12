package providers

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"github.com/klauspost/compress/zstd"
)

type BodyReader = io.Reader

type requestPayloadHashKey struct{}

// WithRequestPayloadHash binds the exact post-transform wire body's SHA-256 to
// the auth-mapping call. Signing adapters use it so credentials cover the bytes
// that will actually be sent upstream, without rereading or retaining the body.
func WithRequestPayloadHash(ctx context.Context, body []byte) context.Context {
	sum := sha256.Sum256(body)
	return context.WithValue(ctx, requestPayloadHashKey{}, hex.EncodeToString(sum[:]))
}

// RequestPayloadHash returns a hash installed by WithRequestPayloadHash.
func RequestPayloadHash(ctx context.Context) (string, bool) {
	hash, ok := ctx.Value(requestPayloadHashKey{}).(string)
	return hash, ok && len(hash) == sha256.Size*2
}

type Credential struct {
	Mode string
	Key  string
	// AuthFallbackEnv is the sole environment variable the resolver permits the
	// standalone gateway to consult when replacing the explicit placeholder
	// credential (Bearer no-key-required). Empty means that no environment
	// fallback is allowed. This is resolver-owned policy: the gateway must never
	// infer a provider-wide secret from only the adapter name, because named
	// OpenAI-compatible routes can intentionally be unauthenticated or use a
	// different, route-scoped env key.
	AuthFallbackEnv string
	// AuthKind separates how a provider credential is paid for/authenticated from
	// its wire header. Bedrock API keys are PAYG credentials transported as a
	// bearer, while IAM access keys require SigV4; treating every bearer as OAuth
	// would suppress real Bedrock spend and select the wrong forward policy.
	//
	// Known Bedrock values are "bedrock_api_key" and "aws_access_keys". Empty
	// preserves legacy credential-shape compatibility inside the Bedrock adapter.
	AuthKind string
	// Scheme records how the key arrived when it was passed through from the
	// inbound request: "bearer" means the agent sent `Authorization: Bearer`
	// (e.g. Claude Pro/Max OAuth tokens, which Anthropic only accepts as a
	// Bearer — remapping them to x-api-key guarantees a 401). Empty means an
	// operator/BYOK key with the provider's default header. Resolvers that
	// don't set it get the pre-existing mapping unchanged.
	Scheme string
}

type RequestMetadata struct {
	Provider string
	Model    string
	// Region is the provider billing region resolved from a trusted route/path,
	// never a client body field. Region-sensitive callers must use an exact
	// catalog row rather than silently borrowing another region's price.
	Region       string
	ServiceTier  string
	InferenceGeo string
	// BillingTier is trusted connection configuration, not a request-body hint.
	// It is required for providers (currently the Gemini Developer API) where an
	// API-key shape alone cannot distinguish free-tier from paid-tier traffic.
	BillingTier string
	// PricingUnsupportedReason is non-empty when the request uses a priced
	// dimension the catalog does not model (for example audio token buckets).
	// Downstream spend must then fail closed to an explicitly unpriced zero.
	PricingUnsupportedReason string
	Endpoint                 string
	Stream                   bool
	InputBytes               int
	MessageCount             int
	ToolsCount               int
	// SessionID is the caller-supplied x-cave-session value, set by the STANDALONE
	// proxy after InspectRequest (the managed gateway leaves it empty). It is
	// content-blind routing input for session-scoped provider metadata such as
	// OpenAI's prompt_cache_key, and it is never forwarded upstream verbatim —
	// consumers hash it, because a caller-supplied token may carry user content.
	// It never affects pricing, usage parsing, or any savings figure.
	SessionID string
}

// ListPriceEligible separates authentication safety from payment semantics.
// OAuth stays lossless/stealth at the forwarding layer, but Google Cloud Vertex
// OAuth/ADC calls are still billed to a project. Subscription/session traffic
// and ambiguous credentials never receive a guessed marginal price.
func ListPriceEligible(provider, authMode string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	authMode = strings.ToLower(strings.TrimSpace(authMode))
	switch authMode {
	case "payg", "api_key":
		return true
	case "oauth":
		return provider == "vertex"
	default:
		return false
	}
}

// ApplyResolvedPricingRoute adds pricing metadata derived from the trusted,
// resolved upstream URL. Client body/header hints cannot override the actual
// billing route selected by the gateway.
func ApplyResolvedPricingRoute(meta RequestMetadata, upstream *url.URL) RequestMetadata {
	if upstream == nil {
		return meta
	}
	host := strings.ToLower(upstream.Hostname())
	switch meta.Provider {
	case "openai":
		switch host {
		case "us.api.openai.com":
			meta.Region = "us"
		case "eu.api.openai.com":
			meta.Region = "eu"
		case "au.api.openai.com":
			meta.Region = "au"
		case "ca.api.openai.com":
			meta.Region = "ca"
		case "jp.api.openai.com":
			meta.Region = "jp"
		case "in.api.openai.com":
			meta.Region = "in"
		case "sg.api.openai.com":
			meta.Region = "sg"
		case "kr.api.openai.com":
			meta.Region = "kr"
		case "gb.api.openai.com":
			meta.Region = "gb"
		case "ae.api.openai.com":
			meta.Region = "ae"
		}
	case "bedrock":
		parts := strings.Split(host, ".")
		for i, part := range parts {
			if (part == "bedrock-runtime" || part == "bedrock-runtime-fips" || part == "bedrock-mantle") && i+1 < len(parts) {
				meta.Region = parts[i+1]
				break
			}
		}
	}
	return meta
}

type TransformPolicy struct {
	RuntimeMode string
	// AuthMode is the classified caller class ("payg", "oauth", or
	// "subscription"). Only the STANDALONE proxy sets it
	// (public/proxy/internal/gateway/proxy.go); the MANAGED gateway does not —
	// it gates non-PAYG traffic in its own policy layer before the adapter
	// seam — so adapter checks on this field are standalone-only guards. Empty
	// therefore means "no claim" (direct adapter tests, managed gateway), not
	// "payg".
	AuthMode string
	// Optimizers gates which provider-native optimizers may run, keyed by
	// optimizer id (e.g. "anthropic-cache-breakpoints"). An optimizer applies
	// only when its flag is true AND RuntimeMode permits transforms.
	Optimizers map[string]bool
	// EvalGates records which optimizers have passed their eval gate. Behavioral
	// optimizers that can change model output (e.g. "output-brevity") require BOTH
	// their Optimizers flag AND a cleared eval gate before they may run.
	EvalGates map[string]bool
	// OptimizerStates is the control-plane-derived activation evidence. Managed
	// transforms require "active"; policy booleans cannot self-authorize them.
	// Standalone callers leave this nil and retain inferred-only behavior.
	OptimizerStates map[string]string
}

// OptimizerEnabled reports whether the named optimizer is enabled by policy.
func (p TransformPolicy) OptimizerEnabled(id string) bool {
	return p.Optimizers[id]
}

// EvalGateCleared reports whether the named optimizer has passed its eval gate.
// Byte-safe optimizers ignore this; behavioral ones must check it.
func (p TransformPolicy) EvalGateCleared(id string) bool {
	return p.EvalGates[id]
}

func (p TransformPolicy) OptimizerActive(id string) bool {
	if p.OptimizerStates == nil {
		// Standalone has no verified ledger, but policy flags and eval gates still
		// guard behavioral transforms. Nil state is not blanket authorization.
		return p.Optimizers[id] && p.EvalGates[id]
	}
	return p.Optimizers[id] && p.EvalGates[id] && p.OptimizerStates[id] == "active"
}

type TransformResult struct {
	Body         []byte
	OptimizerIDs []string
}

type UsageObservation struct {
	// InputTokens and OutputTokens use one provider-neutral contract:
	//
	//   - InputTokens is total effective input, including cache reads/writes.
	//   - OutputTokens is total billed/generated output, including reasoning.
	//   - cache/reasoning fields below are subsets, never additive totals.
	//
	// Providers expose different raw semantics (Anthropic/Bedrock input excludes
	// cache, OpenAI/Gemini input includes it; Gemini candidate output excludes
	// thoughts). Parsers normalize those differences here so downstream displays
	// and aggregations never need provider-specific arithmetic.
	InputTokens              int
	OutputTokens             int
	CachedInputTokens        int
	CacheCreationInputTokens int
	CacheCreation5mTokens    int
	CacheCreation1hTokens    int
	ReasoningTokens          int
	CacheStatus              string
	ProviderRequestID        string
	ServiceTier              string
	InferenceGeo             string
	PricingUnsupportedReason string
	// RawUsage is the provider's usage fields exactly as reported, MERGED
	// across every usage-bearing chunk seen for a non-streaming body or an SSE
	// stream (re-marshaled from the parsed values, so key order/whitespace may
	// differ but every value is preserved verbatim). This is deliberately a
	// merge, not "keep the last chunk": providers split usage across multiple
	// events, and later chunks routinely omit fields earlier ones reported.
	// Anthropic is the concrete case — message_start carries input_tokens and
	// the cache fields, message_delta carries ONLY the final output_tokens —
	// so keeping only the last chunk would silently drop input/cache data from
	// exactly the provider whose cache delta mints verified_savings. A later
	// chunk's null for a key never erases an earlier chunk's non-null value for
	// that same key (mergeRawUsageJSON); when both sides are numbers the LARGER
	// wins, the same max rule the token counters above resolve with. It exists
	// so a later catalog correction can re-price a request from what the
	// provider actually said, without re-deriving through this parser's own
	// normalization (which is exactly the step a bug here would need to
	// bypass) — and that is precisely why the two rules have to agree. It is a
	// display/audit field only: InputTokens/OutputTokens/etc. above remain the
	// values every other code path prices from.
	//
	// When the parser zeroes a typed counter because the stream ended without
	// its terminal usage event, this blob is stamped
	// "raw_usage_incomplete": true (MarkRawUsageIncomplete) — the one key in it
	// the provider did not send. There is NO re-pricing consumer today and
	// nothing currently reads that key; it is forward defense. Any re-pricing
	// path that is added MUST refuse a blob carrying it, or it will resurrect a
	// count this parser has already declared incomplete.
	RawUsage json.RawMessage
	// ObservationCount lets multi-call aggregators preserve incompleteness: one
	// missing call plus one complete call is still partial, never complete.
	ObservationCount int
	// InputTokensReported/OutputTokensReported distinguish a provider-reported
	// zero from missing usage (e.g. an interrupted stream). Malformed is set when
	// a recognized counter is negative, fractional, out of range, contradictory,
	// or cannot be represented as an int. Downstream billing must fail closed on
	// malformed usage and label partial/missing usage explicitly.
	InputTokensReported  bool
	OutputTokensReported bool
	Malformed            bool
	// CacheObserved is true when the provider reported any cache telemetry at
	// all (read or creation). It distinguishes a genuine "miss" from "the
	// provider said nothing about caching" so cache_status is not over-asserted.
	CacheObserved bool
	// CallObservations preserves per-call usage for server-side multi-turn loops.
	// Aggregate token fields remain additive for display, while nonlinear pricing
	// (long-context tiers) is applied to each provider call independently.
	CallObservations []UsageObservation `json:"-"`
}

// Complete reports whether both provider token totals were observed and all
// recognized counters were internally valid. Billing must not extrapolate a
// plausible exact dollar amount from a partial or malformed response.
func (u UsageObservation) Complete() bool {
	return u.InputTokensReported && u.OutputTokensReported && !u.Malformed
}

type ProviderError struct {
	Code    string
	Message string
}

// RouteContext is the per-request routing seam passed to ResolveUpstreamURL. It
// deliberately carries resolved routing hints, not control-plane objects, so the
// adapter set stays shareable between the managed gateway and the public
// standalone proxy.
type RouteContext struct {
	BaseURL      string
	EndpointKind string
}

type Adapter interface {
	Name() string
	MatchRoute(method string, path string) bool
	ResolveUpstreamURL(ctx context.Context, req *http.Request, route RouteContext) (*url.URL, error)
	// SanitizeAndMapHeaders maps the inbound headers to the upstream header set and
	// applies any provider auth/signing. upstream is the ALREADY-RESOLVED forward URL
	// (the exact target the proxy will send to); a signing adapter MUST sign against it
	// so the signed Host matches the wire Host (otherwise SigV4 fails when a per-project
	// base URL differs from the adapter default). Non-signing adapters ignore it.
	SanitizeAndMapHeaders(ctx context.Context, req *http.Request, credential Credential, upstream *url.URL) (http.Header, error)
	InspectRequest(ctx context.Context, body BodyReader, headers http.Header) (RequestMetadata, error)
	ApplyProviderNativeTransforms(ctx context.Context, body BodyReader, meta RequestMetadata, policy TransformPolicy) (TransformResult, error)
	// ExtractCompressible returns only the request's cache-cold live-zone text
	// segments and a reassemble fn that byte-splices replacements into the original
	// body in the SAME order. It is the schema-aware half of S4 compress mode: the
	// proxy runs the engine on each segment, stores the original segment in CCR,
	// appends an in-block marker to compressed replacements, and sends the shortened
	// body upstream. ok=false opts a provider/route out, so compress mode passes the
	// body through unchanged — the byte-safe default (Base returns false).
	ExtractCompressible(body []byte, meta RequestMetadata) (segments [][]byte, reassemble func([][]byte) ([]byte, error), ok bool)
	ParseUsage(ctx context.Context, responseHeaders http.Header, streamOrBody io.Reader) (UsageObservation, io.Reader, error)
	// NewUsageScanner returns a sink that the proxy tees the upstream response
	// through. It captures usage from both whole-body (non-stream) and SSE/JSONL
	// (stream) responses without buffering the client copy, so usage is read
	// after the stream completes via Usage().
	NewUsageScanner(responseHeaders http.Header) *UsageScanner
	MapProviderError(status int, headers http.Header, body []byte) ProviderError
}

// TokenCounter is an optional provider capability for projecting an original
// inference request onto that provider's token-count endpoint. Implementations
// only shape and parse bytes; the gateway owns all network I/O so credentials,
// SSRF policy, and timeouts stay on the existing upstream path.
type TokenCounter interface {
	// CountTokensRequest projects the ORIGINAL request. ok=false means no
	// baseline call may be issued.
	CountTokensRequest(original []byte, meta RequestMetadata) (routePath string, body []byte, ok bool)
	ParseCountTokens(response []byte) (tokens int, ok bool)
}

// RewritableBlock is one content block an adapter exposes for byte-safe rewriting,
// tagged with the provider-cache zone it sits in. Live blocks sit past the cache
// floor and may be newly compressed; frozen blocks sit at or below it and may only
// be replaced byte-identically with a replacement the proxy already emitted for
// those exact bytes — which is what keeps the upstream prefix stable across turns.
type RewritableBlock struct {
	Content []byte
	Live    bool
	// Kind is provider-envelope semantic identity used by compiled Cave Plans.
	// Empty means adapter cannot prove a semantic mapping; locked routes fail open.
	Kind string
	// ID is optional provider-stable identity. Class-level routes leave it empty.
	ID string
}

// recoveryToolSuffix identifies the caveman recovery tool in any namespace an
// agent host may register it under: bare `caveman_retrieve` for the server-side
// injected tool, `mcp__caveman__caveman_retrieve` under Claude Code's MCP
// naming, and whatever prefix another host chooses.
const recoveryToolSuffix = "caveman_retrieve"

// IsRecoveryToolName reports whether a tool call is a caveman recovery call.
//
// It exists so every adapter agrees on one definition. A tool_result produced by
// this tool IS recovered content — the exact original bytes the agent asked back
// after an elision — and re-compressing it is not merely wasteful, it is
// self-defeating: the replacement is a deterministic function of block content
// and is memoised in the prefix cache, so the agent is handed back the very
// elision it was trying to escape. Measured on 2026-08-06: the agent called
// retrieve, reported "Same truncation", and re-read the same file seven times.
func IsRecoveryToolName(name string) bool {
	return name == recoveryToolSuffix || strings.HasSuffix(name, "__"+recoveryToolSuffix)
}

type Base struct {
	Provider string
	BaseURL  string
	Routes   []string
}

func (b Base) Name() string { return b.Provider }

func (b Base) MatchRoute(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	for _, route := range b.Routes {
		// Routes ending in '/' are explicit subtree mounts (for example
		// /compat/ and /azure/). Every other route is exact. Treating all routes
		// as prefixes made /v1/responses-anything and /v1/messages/unknown valid
		// proxy surfaces, violating the gateway's closed route allowlist.
		if path == route || strings.HasSuffix(route, "/") && strings.HasPrefix(path, route) {
			return true
		}
	}
	return false
}

func (b Base) ResolveUpstreamURL(ctx context.Context, req *http.Request, route RouteContext) (*url.URL, error) {
	baseURL := b.BaseURL
	if route.BaseURL != "" {
		baseURL = route.BaseURL
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("provider %q has no configured upstream URL", b.Provider)
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if !base.IsAbs() || base.Hostname() == "" {
		return nil, fmt.Errorf("provider %q upstream URL must be absolute", b.Provider)
	}
	path := req.URL.Path
	for _, prefix := range []string{"/openai", "/anthropic", "/gemini", "/compat/stub", "/compat/openai-compatible"} {
		path = strings.TrimPrefix(path, prefix)
	}
	if strings.HasPrefix(req.URL.Path, "/azure/") {
		path = strings.TrimPrefix(req.URL.Path, "/azure")
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	base.RawQuery = req.URL.RawQuery
	return base, nil
}

func (b Base) SanitizeAndMapHeaders(ctx context.Context, req *http.Request, credential Credential, _ *url.URL) (http.Header, error) {
	out := http.Header{}
	copyIfPresent(out, req.Header, "content-type")
	copyIfPresent(out, req.Header, "accept")
	copyIfPresent(out, req.Header, "accept-encoding")
	copyIfPresent(out, req.Header, "idempotency-key")
	copyIfPresent(out, req.Header, "openai-organization")
	copyIfPresent(out, req.Header, "openai-project")
	copyIfPresent(out, req.Header, "anthropic-version")
	copyIfPresent(out, req.Header, "anthropic-beta")
	copyIfPresent(out, req.Header, "api-version")
	if env.Bool("CAVE_PROPAGATE_TRACE_HEADERS_UPSTREAM", false) {
		copyIfPresent(out, req.Header, "traceparent")
		copyIfPresent(out, req.Header, "tracestate")
	}
	out.Set("user-agent", appendUserAgent(req.UserAgent()))
	switch b.Provider {
	case "anthropic":
		if credential.Key != "" {
			if credential.Scheme == "bearer" {
				// Scheme-preserving pass-through: the agent authenticated with
				// `Authorization: Bearer` (Claude Pro/Max OAuth or an
				// ANTHROPIC_AUTH_TOKEN setup) — forward it the way it arrived;
				// OAuth tokens are rejected when remapped to x-api-key.
				out.Set("authorization", "Bearer "+credential.Key)
			} else {
				out.Set("x-api-key", credential.Key)
			}
		}
		if out.Get("anthropic-version") == "" {
			out.Set("anthropic-version", "2023-06-01")
		}
	case "gemini":
		if credential.Key != "" {
			if credential.Scheme == "bearer" {
				// Standalone env fallback uses this synthetic marker to reach the
				// post-sanitization key resolver. It is not OAuth and must not be
				// forwarded or subjected to OAuth quota-project validation.
				if credential.Key == "no-key-required" {
					break
				}
				// Gemini CLI OAuth/ADC credentials are bearer tokens. Remapping one to
				// x-goog-api-key breaks authentication and silently defeats OAuth wrap.
				// Google also requires the caller's quota project for user OAuth. Never
				// guess it: wrong attribution is a billing and quota correctness bug.
				quotaProject := strings.TrimSpace(req.Header.Get("x-goog-user-project"))
				if !validGoogleQuotaProject(quotaProject) {
					return nil, fmt.Errorf("gemini bearer credential requires a valid x-goog-user-project")
				}
				out.Set("authorization", "Bearer "+credential.Key)
				out.Set("x-goog-user-project", quotaProject)
			} else {
				out.Set("x-goog-api-key", credential.Key)
			}
		}
	case "vertex":
		if credential.Key != "" {
			out.Set("authorization", "Bearer "+credential.Key)
		}
		copyIfPresent(out, req.Header, "x-vertex-ai-llm-request-type")
		copyIfPresent(out, req.Header, "x-goog-user-project")
	case "azure_openai":
		if credential.Key != "" {
			// The standalone placeholder is a synthetic no-key marker used by
			// callers that explicitly allow env-backed API-key fallback. It is
			// not an Entra token and must not be forwarded as Authorization;
			// gateway fallback may replace it with the Azure api-key header.
			if credential.Scheme == "bearer" && credential.Key == "no-key-required" {
				break
			}
			// Real bearer/JWT credentials remain fail-closed until an auth-kind is
			// persisted and mapped end-to-end. They must never be silently
			// relabeled as an api-key.
			fields := strings.Fields(strings.TrimSpace(credential.Key))
			if credential.Scheme == "bearer" || len(fields) > 1 && strings.EqualFold(fields[0], "bearer") || strings.HasPrefix(strings.TrimSpace(credential.Key), "eyJ") {
				return nil, fmt.Errorf("azure bearer credentials are unsupported; use an API key")
			}
			out.Set("api-key", credential.Key)
		}
	default:
		if credential.Key != "" {
			out.Set("authorization", "Bearer "+credential.Key)
		}
	}
	return out, nil
}

func validGoogleQuotaProject(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func (b Base) InspectRequest(ctx context.Context, body BodyReader, headers http.Header) (RequestMetadata, error) {
	data, _ := io.ReadAll(body)
	meta := RequestMetadata{Provider: b.Provider, InputBytes: len(data), Endpoint: b.Provider}
	var decoded map[string]any
	if json.Unmarshal(data, &decoded) == nil {
		if model, ok := decoded["model"].(string); ok {
			meta.Model = model
		}
		if stream, ok := decoded["stream"].(bool); ok {
			meta.Stream = stream
		}
		if tier, present, valid := serviceTierFromObject(decoded); present {
			if valid {
				meta.ServiceTier = tier
			} else {
				meta.PricingUnsupportedReason = "unsupported_service_tier_shape"
			}
		}
		if geo, ok := decoded["inference_geo"].(string); ok {
			meta.InferenceGeo = strings.TrimSpace(geo)
		}
		if reason := requestPricingUnsupportedReason(b.Provider, decoded); reason != "" {
			meta.PricingUnsupportedReason = reason
		}
		if messages, ok := decoded["messages"].([]any); ok {
			meta.MessageCount = len(messages)
		}
		if input, ok := decoded["input"].([]any); ok {
			meta.MessageCount = len(input)
		}
		if tools, ok := decoded["tools"].([]any); ok {
			meta.ToolsCount = len(tools)
		}
	}
	if meta.Model == "" {
		meta.Model = modelFromPath(headers.Get("x-cave-route-path"))
	}
	return meta, nil
}

func (b Base) ApplyProviderNativeTransforms(ctx context.Context, body BodyReader, meta RequestMetadata, transformPolicy TransformPolicy) (TransformResult, error) {
	data, _ := io.ReadAll(body)
	return TransformResult{Body: data, OptimizerIDs: []string{}}, nil
}

// ExtractCompressible opts out by default: a provider without schema-aware content
// extraction must never have compress mode reshape its body.
func (b Base) ExtractCompressible(body []byte, meta RequestMetadata) ([][]byte, func([][]byte) ([]byte, error), bool) {
	return nil, nil, false
}

func (b Base) ParseUsage(ctx context.Context, responseHeaders http.Header, streamOrBody io.Reader) (UsageObservation, io.Reader, error) {
	data, err := io.ReadAll(streamOrBody)
	usage := UsageObservation{CacheStatus: "unknown", ProviderRequestID: providerRequestID(responseHeaders), ServiceTier: responseServiceTier(responseHeaders), ObservationCount: 1}
	if err != nil {
		return usage, bytes.NewReader(data), err
	}
	accountingBody, reason := decodeAccountingBody(data, responseHeaders.Get("Content-Encoding"), env.Int("CAVE_USAGE_SCAN_MAX_BYTES", 16*1024*1024))
	if reason != "" {
		usage.PricingUnsupportedReason = reason
		return usage, bytes.NewReader(data), nil
	}
	ParseUsageBytes(b.Provider, accountingBody, &usage)
	usage.CacheStatus = cacheStatusFor(usage)
	return usage, bytes.NewReader(data), nil
}

func (b Base) NewUsageScanner(responseHeaders http.Header) *UsageScanner {
	return &UsageScanner{
		provider:    b.Provider,
		requestID:   providerRequestID(responseHeaders),
		serviceTier: responseServiceTier(responseHeaders),
		encoding:    responseHeaders.Get("Content-Encoding"),
		limit:       env.Int("CAVE_USAGE_SCAN_MAX_BYTES", 2*1024*1024),
	}
}

// UsageScanner accumulates response bytes as they stream to the client and
// extracts provider usage once the copy completes. Write never errors so it can
// sit on an io.TeeReader path without disrupting the client copy. If the body
// exceeds limit it stops buffering and reports unknown usage rather than risk
// unbounded memory.
type UsageScanner struct {
	provider    string
	requestID   string
	serviceTier string
	encoding    string
	limit       int
	buf         bytes.Buffer
	tail        bytes.Buffer
	truncated   bool
	// parse, when set, replaces the built-in provider parser. Adapters whose
	// response shape is not understood by ParseUsageBytes (e.g. Bedrock camelCase
	// usage) supply their own; it must resolve CacheStatus itself.
	parse func(data []byte, usage *UsageObservation)
}

// NewExternalUsageScanner returns a UsageScanner that uses a caller-supplied
// parser instead of the built-in ParseUsageBytes path. It is the constructor an
// adapter in another package uses to plug a provider-specific usage parser into
// the streaming scan. The parser is responsible for setting CacheStatus.
func NewExternalUsageScanner(responseHeaders http.Header, parse func(data []byte, usage *UsageObservation)) *UsageScanner {
	return &UsageScanner{
		requestID:   providerRequestID(responseHeaders),
		serviceTier: responseServiceTier(responseHeaders),
		encoding:    responseHeaders.Get("Content-Encoding"),
		limit:       env.Int("CAVE_USAGE_SCAN_MAX_BYTES", 2*1024*1024),
		parse:       parse,
	}
}

// SetLimit overrides the max bytes the scanner will buffer before reporting
// unknown usage. Used by adapters that read their own configured limit.
func (s *UsageScanner) SetLimit(limit int) { s.limit = limit }

// SetRequestID overrides the provider request id recorded with the usage (e.g.
// when the provider returns it under a non-default header).
func (s *UsageScanner) SetRequestID(id string) {
	if id != "" {
		s.requestID = id
	}
}

func (s *UsageScanner) Write(p []byte) (int, error) {
	if !s.truncated {
		// Written this way rather than buf.Len()+len(p) so an adversarially large
		// slice cannot overflow int and bypass the configured cap.
		if s.limit <= 0 || len(p) > s.limit-s.buf.Len() {
			s.truncated = true
			s.appendTail(s.buf.Bytes())
			s.appendTail(p)
			// Drop the full-prefix backing array after transferring its bounded
			// tail. Reset would retain up to limit bytes for the stream lifetime.
			s.buf = bytes.Buffer{}
		} else {
			s.buf.Write(p)
		}
	} else {
		s.appendTail(p)
	}
	return len(p), nil
}

func (s *UsageScanner) appendTail(p []byte) {
	tailLimit := s.limit
	if tailLimit > 1024*1024 {
		tailLimit = 1024 * 1024
	}
	if tailLimit <= 0 || len(p) >= tailLimit {
		s.tail.Reset()
		if tailLimit > 0 {
			s.tail.Write(p[len(p)-tailLimit:])
		}
		return
	}
	over := s.tail.Len() + len(p) - tailLimit
	if over > 0 {
		kept := append([]byte(nil), s.tail.Bytes()[over:]...)
		s.tail.Reset()
		s.tail.Write(kept)
	}
	s.tail.Write(p)
}

func (s *UsageScanner) Usage() UsageObservation {
	usage := UsageObservation{CacheStatus: "unknown", ProviderRequestID: s.requestID, ServiceTier: s.serviceTier, ObservationCount: 1}
	data := s.buf.Bytes()
	if s.truncated {
		// A complete terminal SSE/JSONL usage event is often at the end of a very
		// large response. Recover it from a bounded tail only for identity encoding
		// and only if the provider parser proves both input and output counters.
		if normalizedContentEncoding(s.encoding) != "identity" {
			usage.PricingUnsupportedReason = "response_scan_limit_exceeded"
			return usage
		}
		data = s.tail.Bytes()
	}
	accountingBody, reason := decodeAccountingBody(data, s.encoding, s.limit)
	if reason != "" {
		usage.PricingUnsupportedReason = reason
		return usage
	}
	if s.parse != nil {
		s.parse(accountingBody, &usage)
		if s.truncated && !usage.Complete() {
			return UsageObservation{CacheStatus: "unknown", ProviderRequestID: s.requestID, ServiceTier: s.serviceTier, ObservationCount: 1, PricingUnsupportedReason: "response_scan_limit_exceeded"}
		}
		return usage
	}
	ParseUsageBytes(s.provider, accountingBody, &usage)
	if s.truncated && !usage.Complete() {
		return UsageObservation{CacheStatus: "unknown", ProviderRequestID: s.requestID, ServiceTier: s.serviceTier, ObservationCount: 1, PricingUnsupportedReason: "response_scan_limit_exceeded"}
	}
	usage.CacheStatus = cacheStatusFor(usage)
	return usage
}

func normalizedContentEncoding(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "identity"
	}
	return value
}

// decodeAccountingBody decodes only the scanner's bounded side-copy. The raw
// provider bytes continue to the client unchanged. Both compressed and decoded
// representations are capped, preventing response compression from becoming an
// accounting bypass or a decompression-memory attack.
func decodeAccountingBody(raw []byte, contentEncoding string, limit int) ([]byte, string) {
	encoding := normalizedContentEncoding(contentEncoding)
	if encoding == "identity" {
		if limit <= 0 || len(raw) > limit {
			return nil, "response_scan_limit_exceeded"
		}
		return raw, ""
	}
	if strings.Contains(encoding, ",") {
		return nil, "unsupported_content_encoding/stacked"
	}
	var reader io.ReadCloser
	switch encoding {
	case "gzip", "x-gzip":
		decoded, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, "content_decode_failed/gzip"
		}
		reader = decoded
	case "deflate":
		decoded, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, "content_decode_failed/deflate"
		}
		reader = decoded
	case "zstd":
		maxMemory := uint64(limit)
		if maxMemory < 1024*1024 {
			maxMemory = 1024 * 1024
		}
		decoded, err := zstd.NewReader(bytes.NewReader(raw), zstd.WithDecoderMaxMemory(maxMemory))
		if err != nil {
			return nil, "content_decode_failed/zstd"
		}
		reader = decoded.IOReadCloser()
	default:
		return nil, "unsupported_content_encoding/" + encoding
	}
	defer reader.Close()
	if limit <= 0 {
		return nil, "response_scan_limit_exceeded"
	}
	decoded, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, "content_decode_failed/" + encoding
	}
	if len(decoded) > limit {
		return nil, "response_scan_limit_exceeded"
	}
	return decoded, ""
}

// ParseUsageBytes extracts token usage from a complete provider response body,
// whether a single JSON document (non-stream) or an SSE / newline-delimited JSON
// stream (where usage is emitted across one or more events). Values are merged
// with a max rule so cumulative stream counters resolve to their final totals.
func ParseUsageBytes(provider string, data []byte, usage *UsageObservation) {
	if root, err := decodeUsageValue(data); err == nil {
		mergeUsageValue(provider, root, usage)
		if _, streamedArray := root.([]any); streamedArray && (provider == "gemini" || provider == "vertex") && !hasGeminiFinishReason(root) && !hasGeminiPromptBlock(root) {
			usage.OutputTokens = 0
			usage.OutputTokensReported = false
			usage.ReasoningTokens = 0
			MarkRawUsageIncomplete(usage)
		}
		return
	}
	var anthropicStream, anthropicFinalUsage bool
	var geminiStream, geminiTerminal bool
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Size the max token to the (already memory-capped) body so a single large
	// SSE/JSONL line — e.g. a big tool-arg or inlined-content event carrying the
	// final usage — is never silently dropped by bufio.ErrTooLong.
	maxLine := len(data) + 1
	if maxLine < 64*1024 {
		maxLine = 64 * 1024
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, ":") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if line == "" || line == "[DONE]" {
			continue
		}
		var obj map[string]any
		if decodeUsageObject([]byte(line), &obj) == nil {
			typ, _ := obj["type"].(string)
			switch typ {
			case "message_start":
				anthropicStream = true
			case "message_delta":
				anthropicStream = true
				if u, ok := obj["usage"].(map[string]any); ok {
					_, anthropicFinalUsage = u["output_tokens"]
				}
			case "message_stop":
				anthropicStream = true
			}
			if provider == "gemini" || provider == "vertex" {
				if _, ok := obj["usageMetadata"]; ok {
					geminiStream = true
				}
				geminiTerminal = geminiTerminal || hasGeminiFinishReason(obj) || hasGeminiPromptBlock(obj)
			}
			mergeUsage(provider, obj, usage)
		}
	}
	if anthropicStream && !anthropicFinalUsage {
		// message_start carries only a provisional output count. Without the
		// terminal message_delta usage block (truncation, error, cancelled copy),
		// output and total spend are not provider-complete.
		usage.OutputTokens = 0
		usage.OutputTokensReported = false
		usage.ReasoningTokens = 0
		MarkRawUsageIncomplete(usage)
	}
	if geminiStream && !geminiTerminal {
		usage.OutputTokens = 0
		usage.OutputTokensReported = false
		usage.ReasoningTokens = 0
		MarkRawUsageIncomplete(usage)
	}
}

func hasGeminiFinishReason(value any) bool {
	if chunks, ok := value.([]any); ok {
		for _, chunk := range chunks {
			if hasGeminiFinishReason(chunk) {
				return true
			}
		}
		return false
	}
	root, ok := value.(map[string]any)
	if !ok {
		return false
	}
	candidates, ok := root["candidates"].([]any)
	if !ok {
		return false
	}
	for _, raw := range candidates {
		candidate, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if reason, ok := candidate["finishReason"].(string); ok && strings.TrimSpace(reason) != "" {
			return true
		}
	}
	return false
}

func hasGeminiPromptBlock(value any) bool {
	if chunks, ok := value.([]any); ok {
		for _, chunk := range chunks {
			if hasGeminiPromptBlock(chunk) {
				return true
			}
		}
		return false
	}
	root, ok := value.(map[string]any)
	if !ok {
		return false
	}
	feedback, ok := root["promptFeedback"].(map[string]any)
	if !ok {
		return false
	}
	reason, _ := feedback["blockReason"].(string)
	return strings.TrimSpace(reason) != ""
}

func mergeUsageValue(provider string, value any, usage *UsageObservation) {
	switch node := value.(type) {
	case map[string]any:
		mergeUsage(provider, node, usage)
	case []any:
		for _, item := range node {
			mergeUsageValue(provider, item, usage)
		}
	}
}

func decodeUsageValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func decodeUsageObject(data []byte, dst *map[string]any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

// mergeUsage reads every usage-bearing sub-object reachable from obj and merges
// cumulative stream counters with a max rule. Each object is normalized to the
// provider-neutral totals documented on UsageObservation before it is merged.
func mergeUsage(provider string, obj map[string]any, usage *UsageObservation) {
	mergePricingQualifiers(obj, usage)
	mergeProviderOutcomeQualifiers(provider, obj, usage)
	if resp, ok := obj["response"].(map[string]any); ok {
		// OpenAI Responses stream events put the authoritative service_tier on
		// response.completed.response, beside (not inside) response.usage.
		mergePricingQualifiers(resp, usage)
		mergeProviderOutcomeQualifiers(provider, resp, usage)
	}
	for _, u := range usageObjects(obj) {
		if hasGeminiUsageKeys(u) {
			mergeGeminiUsage(u, usage)
		} else {
			mergeSnakeUsage(provider, u, usage)
		}
		mergePricingQualifiers(u, usage)
		// Deep-merge, not overwrite: a provider can split usage across
		// multiple chunks (Anthropic's message_start carries input_tokens +
		// cache fields, message_delta carries only the final output_tokens),
		// so keeping only the last chunk would silently drop fields only an
		// earlier chunk reported. See the RawUsage field doc for why this
		// matters specifically for Anthropic.
		usage.RawUsage = mergeRawUsageJSON(usage.RawUsage, u)
	}
	if (provider == "gemini" || provider == "vertex") && !usage.OutputTokensReported {
		if _, filtered := obj["promptFeedback"]; filtered && usage.InputTokensReported {
			// A filtered Gemini response can omit candidatesTokenCount entirely.
			// promptFeedback with no candidate proves generated output is zero;
			// provider-reported prompt input remains billable.
			usage.OutputTokens = 0
			usage.OutputTokensReported = true
		}
	}
}

// mergeRawUsageJSON deep-merges next into the map decoded from existing and
// re-marshals it. A key in next whose value is JSON null is skipped rather
// than merged, so a later chunk's null (e.g. Anthropic's terminal
// message_delta reporting nothing about input/cache tokens at all — those
// keys are simply absent from it, but a provider that DOES emit an explicit
// null for a field it once reported must not erase the earlier good value)
// never erases an earlier chunk's real value for that same key. When both
// sides of a key are numbers the LARGER wins, which is exactly how the typed
// token counters above resolve cumulative stream counters (usage.InputTokens =
// max(...)). Non-numeric values are last-non-null-wins, since there is no
// ordering to apply to a service tier or a request id.
func mergeRawUsageJSON(existing json.RawMessage, next map[string]any) json.RawMessage {
	merged := map[string]any{}
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &merged)
	}
	mergeRawUsageMap(merged, next)
	out, err := json.Marshal(merged)
	if err != nil {
		return existing
	}
	return out
}

// mergeRawUsageMap applies the max rule for numeric counters.
//
// It used to be last-non-null-wins while its own doc claimed it matched the
// typed counters' max rule — and the two genuinely diverge on a non-monotonic
// stream (a provider re-reporting a smaller value in a later chunk, or a
// truncated tail whose first surviving chunk carries a partial count). RawUsage
// exists so a later catalog correction can re-price a request from what the
// provider actually said; if it disagreed with the typed counters the request
// was priced from, re-pricing would silently produce a different number than
// the original. Max is the rule that keeps the two in agreement.
func mergeRawUsageMap(dst, src map[string]any) {
	for k, v := range src {
		if v == nil {
			continue
		}
		if srcSub, ok := v.(map[string]any); ok {
			if dstSub, ok := dst[k].(map[string]any); ok {
				mergeRawUsageMap(dstSub, srcSub)
				continue
			}
		}
		if existing, present := dst[k]; present {
			if larger, comparable := largerNumber(existing, v); comparable {
				dst[k] = larger
				continue
			}
		}
		dst[k] = v
	}
}

// largerNumber returns whichever of a/b is the larger number. comparable is
// false when either side is not a number (or is a number this cannot read), and
// the caller falls back to last-non-null-wins.
func largerNumber(a, b any) (any, bool) {
	af, aok := usageNumber(a)
	bf, bok := usageNumber(b)
	if !aok || !bok {
		return nil, false
	}
	if af >= bf {
		return a, true
	}
	return b, true
}

// usageNumber reads the numeric representations raw usage values arrive in.
// Values parsed from the wire are json.Number (decoders here use UseNumber);
// values round-tripped through the merged blob's re-marshal come back float64.
func usageNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// RawUsageIncompleteKey marks a RawUsage blob the parser has declared not
// provider-complete. It is the one key in that blob the provider did not send.
const RawUsageIncompleteKey = "raw_usage_incomplete"

// MarkRawUsageIncomplete stamps RawUsage when a parser has just zeroed a typed
// counter because the stream ended without its terminal usage event.
//
// Without the stamp the two disagree in the most dangerous direction: the typed
// OutputTokens is honestly zeroed and OutputTokensReported false, while RawUsage
// still carries the provisional output_tokens from message_start. Re-pricing
// from RawUsage would then resurrect a count this parser has already declared
// incomplete, quietly turning an unpriceable request into a priced one. If the
// blob cannot be stamped it is dropped instead — an absent raw blob is honest,
// an unlabelled partial one is not.
//
// EXPORTED because adapters in other packages detect truncation this package
// cannot see. Bedrock is the concrete case: its EventStream and Mantle-SSE
// parsers find the missing terminal frame themselves and then hand
// ParseUsageBytes a single synthesized {"usage":{...}} document, which carries
// none of the message_start/message_delta markers the stamping inside this file
// keys on. Every parser that zeroes OutputTokensReported MUST call this, or the
// contract stated on UsageObservation.RawUsage and in
// db/clickhouse/migrations/0025_raw_provider_usage.sql — absence of the key
// means the blob is complete — is false for that provider, and a re-pricer
// obeying the documented rule resurrects a count the parser refused.
//
// The stamp is never cleared. Callers that reuse one UsageObservation across
// several parse calls (bedrock/usage.go's multi-turn loop) therefore keep it
// once any call sets it, which is the correct direction: one incomplete
// observation in an aggregate makes the aggregate incomplete.
func MarkRawUsageIncomplete(usage *UsageObservation) {
	if len(usage.RawUsage) == 0 {
		return
	}
	var decoded map[string]any
	if json.Unmarshal(usage.RawUsage, &decoded) != nil {
		usage.RawUsage = nil
		return
	}
	decoded[RawUsageIncompleteKey] = true
	out, err := json.Marshal(decoded)
	if err != nil {
		usage.RawUsage = nil
		return
	}
	usage.RawUsage = out
}

func hasGeminiUsageKeys(u map[string]any) bool {
	for _, key := range []string{"promptTokenCount", "candidatesTokenCount", "cachedContentTokenCount", "thoughtsTokenCount", "toolUsePromptTokenCount", "totalTokenCount"} {
		if _, ok := u[key]; ok {
			return true
		}
	}
	return false
}

func mergeGeminiUsage(u map[string]any, usage *UsageObservation) {
	prompt, promptOK := usageInt(u, "promptTokenCount", usage)
	toolInput, toolOK := usageInt(u, "toolUsePromptTokenCount", usage)
	candidates, candidatesOK := usageInt(u, "candidatesTokenCount", usage)
	thoughts, thoughtsOK := usageInt(u, "thoughtsTokenCount", usage)
	cached, cachedOK := usageInt(u, "cachedContentTokenCount", usage)
	total, totalOK := usageInt(u, "totalTokenCount", usage)

	input, inputOK := checkedSum(prompt, promptOK, toolInput, toolOK, usage)
	output, outputOK := checkedSum(candidates, candidatesOK, thoughts, thoughtsOK, usage)
	// promptTokenCount and candidatesTokenCount are the required anchors. Optional
	// tool/thought fields augment them when present but their absence is not partial.
	if promptOK && inputOK {
		usage.InputTokensReported = true
		usage.InputTokens = max(usage.InputTokens, input)
	}
	if candidatesOK && outputOK {
		usage.OutputTokensReported = true
		usage.OutputTokens = max(usage.OutputTokens, output)
	}
	if cachedOK {
		usage.CacheObserved = true
		usage.CachedInputTokens = max(usage.CachedInputTokens, cached)
	}
	if thoughtsOK {
		usage.ReasoningTokens = max(usage.ReasoningTokens, thoughts)
	}
	if promptOK && cachedOK && cached > prompt {
		usage.Malformed = true
	}
	if totalOK && promptOK && candidatesOK && inputOK && outputOK {
		// Google documents totalTokenCount inconsistently across Gemini and
		// Vertex surfaces: some totals include toolUsePromptTokenCount while
		// others do not. Both are provider-issued totals; reject only when the
		// value matches neither documented shape.
		withTools, okWithTools := checkedAdd(input, output)
		withoutTools, okWithoutTools := checkedAdd(prompt, output)
		if (!okWithTools || total != withTools) && (!okWithoutTools || total != withoutTools) {
			usage.Malformed = true
		}
	}
	if !candidatesOK && totalOK && inputOK && total == input {
		usage.OutputTokens = 0
		usage.OutputTokensReported = true
	}
}

func mergeSnakeUsage(provider string, u map[string]any, usage *UsageObservation) {
	rawInput, inputOK := aliasUsageInt(u, []string{"input_tokens", "prompt_tokens"}, usage)
	rawOutput, outputOK := aliasUsageInt(u, []string{"output_tokens", "completion_tokens"}, usage)
	cached, cachedOK := aliasUsageInt(u, []string{"cached_input_tokens", "cache_read_input_tokens"}, usage)
	cacheCreate, cacheCreateOK := usageInt(u, "cache_creation_input_tokens", usage)

	for _, detailKey := range []string{"input_tokens_details", "prompt_tokens_details"} {
		if d, ok := u[detailKey].(map[string]any); ok {
			if v, valid := usageInt(d, "cached_tokens", usage); valid {
				cached, cachedOK = mergeAliasValue(cached, cachedOK, v, usage)
			}
			// GPT-5.6 and later OpenAI models report billable prompt-cache
			// writes in this nested field. Normalize it into the provider-neutral
			// cache-creation bucket so costBreakdown prices it at the catalog's
			// cache-write rate instead of silently treating it as ordinary input.
			if v, valid := usageInt(d, "cache_write_tokens", usage); valid {
				cacheCreate, cacheCreateOK = mergeAliasValue(cacheCreate, cacheCreateOK, v, usage)
			}
			if positiveUsageCounter(d, "audio_tokens", usage) {
				usage.PricingUnsupportedReason = "unsupported_audio_tokens"
			}
		}
	}

	reasoning, reasoningOK := usageInt(u, "reasoning_tokens", usage)
	for _, detailKey := range []string{"output_tokens_details", "completion_tokens_details"} {
		if d, ok := u[detailKey].(map[string]any); ok {
			if positiveUsageCounter(d, "audio_tokens", usage) {
				usage.PricingUnsupportedReason = "unsupported_audio_tokens"
			}
			for _, key := range []string{"reasoning_tokens", "thinking_tokens"} {
				if v, valid := usageInt(d, key, usage); valid {
					reasoning, reasoningOK = mergeAliasValue(reasoning, reasoningOK, v, usage)
				}
			}
		}
	}

	var create5m, create1h int
	var create5mOK, create1hOK bool
	if d, ok := u["cache_creation"].(map[string]any); ok {
		create5m, create5mOK = usageInt(d, "ephemeral_5m_input_tokens", usage)
		create1h, create1hOK = usageInt(d, "ephemeral_1h_input_tokens", usage)
		detailTotal, detailOK := checkedSum(create5m, create5mOK, create1h, create1hOK, usage)
		if detailOK {
			if cacheCreateOK && detailTotal != cacheCreate {
				usage.Malformed = true
			}
			if !cacheCreateOK {
				cacheCreate, cacheCreateOK = detailTotal, true
			}
		}
	}

	totalInput := rawInput
	totalInputOK := inputOK
	if providerInputExcludesCache(provider) && inputOK {
		var ok bool
		totalInput, ok = checkedAdd(rawInput, optionalInt(cached, cachedOK))
		if ok {
			totalInput, ok = checkedAdd(totalInput, optionalInt(cacheCreate, cacheCreateOK))
		}
		if !ok {
			usage.Malformed = true
			totalInputOK = false
		}
	}
	if totalInputOK {
		usage.InputTokensReported = true
		usage.InputTokens = max(usage.InputTokens, totalInput)
	}
	if outputOK {
		usage.OutputTokensReported = true
		usage.OutputTokens = max(usage.OutputTokens, rawOutput)
	}
	if cachedOK {
		usage.CacheObserved = true
		usage.CachedInputTokens = max(usage.CachedInputTokens, cached)
	}
	if cacheCreateOK {
		usage.CacheObserved = true
		usage.CacheCreationInputTokens = max(usage.CacheCreationInputTokens, cacheCreate)
	}
	if create5mOK {
		usage.CacheCreation5mTokens = max(usage.CacheCreation5mTokens, create5m)
	}
	if create1hOK {
		usage.CacheCreation1hTokens = max(usage.CacheCreation1hTokens, create1h)
	}
	if reasoningOK {
		usage.ReasoningTokens = max(usage.ReasoningTokens, reasoning)
	}
	if totalInputOK {
		cacheTotal, ok := checkedAdd(optionalInt(cached, cachedOK), optionalInt(cacheCreate, cacheCreateOK))
		if !ok || cacheTotal > totalInput {
			usage.Malformed = true
		}
	}
	if outputOK && reasoningOK && reasoning > rawOutput {
		usage.Malformed = true
	}
	if total, totalOK := usageInt(u, "total_tokens", usage); totalOK && totalInputOK {
		if !outputOK && total == totalInput {
			// Embedding endpoints return prompt/total usage and no generated-output
			// field. Equality is provider-issued proof that output is exactly zero.
			usage.OutputTokensReported = true
		} else if outputOK {
			expected, ok := checkedAdd(totalInput, rawOutput)
			if !ok || total != expected {
				usage.Malformed = true
			}
		}
	}
}

func providerInputExcludesCache(provider string) bool {
	return provider == "anthropic" || provider == "vertex"
}

func usageInt(m map[string]any, key string, usage *UsageObservation) (int, bool) {
	v, present := m[key]
	if !present {
		return 0, false
	}
	if v == nil {
		// A provider explicitly emitted a recognized counter with no numeric
		// value. Treat it as malformed, never as an optional field that happened
		// to be absent; stream terminal detection may otherwise bless a
		// provisional earlier counter as complete.
		usage.Malformed = true
		return 0, false
	}
	n, ok := nonNegativeInt(v)
	if !ok {
		usage.Malformed = true
		return 0, false
	}
	return n, true
}

func aliasUsageInt(m map[string]any, keys []string, usage *UsageObservation) (int, bool) {
	var value int
	found := false
	for _, key := range keys {
		if v, ok := usageInt(m, key, usage); ok {
			value, found = mergeAliasValue(value, found, v, usage)
		}
	}
	return value, found
}

func mergeAliasValue(current int, found bool, next int, usage *UsageObservation) (int, bool) {
	if found && current != next {
		usage.Malformed = true
	}
	if !found || next > current {
		return next, true
	}
	return current, true
}

func nonNegativeInt(v any) (int, bool) {
	var n64 int64
	switch n := v.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return 0, false
		}
		n64 = parsed
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || math.Trunc(n) != n || n >= 9223372036854775808.0 {
			return 0, false
		}
		n64 = int64(n)
	case int:
		if n < 0 {
			return 0, false
		}
		return n, true
	case int64:
		n64 = n
	default:
		return 0, false
	}
	if n64 < 0 || uint64(n64) > uint64(^uint(0)>>1) {
		return 0, false
	}
	return int(n64), true
}

func checkedSum(a int, aOK bool, b int, bOK bool, usage *UsageObservation) (int, bool) {
	if !aOK && !bOK {
		return 0, false
	}
	v, ok := checkedAdd(optionalInt(a, aOK), optionalInt(b, bOK))
	if !ok {
		usage.Malformed = true
		return 0, false
	}
	return v, true
}

func checkedAdd(a, b int) (int, bool) {
	if a < 0 || b < 0 || a > int(^uint(0)>>1)-b {
		return 0, false
	}
	return a + b, true
}

func optionalInt(v int, ok bool) int {
	if !ok {
		return 0
	}
	return v
}

func usageObjects(obj map[string]any) []map[string]any {
	var out []map[string]any
	add := func(v any) {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	add(obj["usage"])
	add(obj["usageMetadata"])
	if msg, ok := obj["message"].(map[string]any); ok {
		add(msg["usage"])
	}
	if resp, ok := obj["response"].(map[string]any); ok {
		add(resp["usage"])
		add(resp["usageMetadata"])
	}
	return out
}

// cacheStatusFor maps observed usage to a cache_status. It only asserts hit/miss
// when the provider actually reported cache telemetry; otherwise it stays
// "unknown" rather than claiming a definitive miss the gateway cannot know.
func cacheStatusFor(usage UsageObservation) string {
	if !usage.CacheObserved || usage.Malformed {
		return "unknown"
	}
	if usage.CachedInputTokens > 0 {
		return "hit"
	}
	if usage.CacheCreationInputTokens > 0 {
		return "write"
	}
	return "miss"
}

func (b Base) MapProviderError(status int, headers http.Header, body []byte) ProviderError {
	return ProviderError{Code: fmt.Sprintf("provider_%d", status), Message: string(body)}
}

func copyIfPresent(dst, src http.Header, name string) {
	if values := src.Values(name); len(values) > 0 {
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}

func appendUserAgent(existing string) string {
	if existing == "" {
		return "caveman-cloud/dev"
	}
	return existing + " caveman-cloud/dev"
}

func modelFromPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "models" && i+1 < len(parts) {
			return strings.Split(parts[i+1], ":")[0]
		}
		if part == "deployments" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return "unknown"
}

func providerRequestID(h http.Header) string {
	for _, key := range []string{"x-request-id", "request-id", "x-goog-request-id", "apim-request-id"} {
		if id := strings.TrimSpace(h.Get(key)); id != "" {
			return id
		}
	}
	return ""
}

func responseServiceTier(h http.Header) string {
	for _, key := range []string{"x-gemini-service-tier", "openai-service-tier", "x-openai-service-tier", "x-amzn-bedrock-service-tier"} {
		if tier := strings.TrimSpace(h.Get(key)); tier != "" {
			return tier
		}
	}
	return ""
}

func requestPricingUnsupportedReason(provider string, decoded map[string]any) string {
	if provider == "openai" {
		if background, _ := decoded["background"].(bool); background {
			return "unsupported_background_response"
		}
	}
	if fallbacks, present := decoded["fallbacks"]; present {
		if values, ok := fallbacks.([]any); !ok || len(values) > 0 {
			return "unsupported_model_fallback_pricing"
		}
	}
	if provider == "bedrock" {
		if _, present := decoded["guardrailConfig"]; present {
			return "unsupported_provider_guardrail_charge"
		}
		if _, present := decoded["guardrail_config"]; present {
			return "unsupported_provider_guardrail_charge"
		}
	}
	for _, key := range []string{"performanceConfig", "performance_config", "performanceConfigLatency", "performance_config_latency"} {
		if value, present := decoded[key]; present {
			latency := ""
			switch typed := value.(type) {
			case string:
				latency = typed
			case map[string]any:
				latency = fmt.Sprint(typed["latency"])
			}
			latency = strings.ToLower(strings.TrimSpace(latency))
			if latency != "" && latency != "standard" {
				return "unsupported_performance_tier"
			}
		}
	}
	if speed, ok := decoded["speed"].(string); ok && !strings.EqualFold(strings.TrimSpace(speed), "standard") && strings.TrimSpace(speed) != "" {
		return "unsupported_speed_tier"
	}
	if formats, ok := decoded["modalities"].([]any); ok {
		for _, format := range formats {
			if value, ok := format.(string); ok && strings.EqualFold(strings.TrimSpace(value), "audio") {
				return "unsupported_audio_tokens"
			}
		}
	}
	// Provider-hosted tools can carry fixed per-call/storage/image charges that
	// token rates cannot reconstruct. Custom/function tools execute outside the
	// provider and remain token-only.
	if tools, ok := decoded["tools"].([]any); ok {
		for _, raw := range tools {
			tool, _ := raw.(map[string]any)
			typ, _ := tool["type"].(string)
			typ = strings.ToLower(strings.TrimSpace(typ))
			if typ != "" && typ != "function" && typ != "custom" && !tokenOnlyProviderTool(provider, typ) {
				return "unsupported_provider_tool_charge"
			}
			// Gemini/Vertex code execution has no separate tool fee; generated
			// code/results are already included in token usage. Grounding, maps,
			// URL context, retrieval, and file search have non-token charges.
			for _, key := range []string{
				"googleSearch", "googleSearchRetrieval", "googleMaps", "urlContext", "fileSearch", "retrieval",
				"google_search", "google_search_retrieval", "google_maps", "url_context", "file_search",
			} {
				if _, present := tool[key]; present {
					return "unsupported_provider_tool_charge"
				}
			}
		}
	}

	// Audio has provider-specific rates and sometimes separate usage buckets.
	// Walk decoded request iteratively so nested Gemini inlineData and OpenAI
	// content parts cannot slip through top-level-only detection.
	stack := []any{decoded}
	for len(stack) > 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch node := v.(type) {
		case map[string]any:
			for key, child := range node {
				lowerKey := strings.ToLower(key)
				if (lowerKey == "cachedcontent" || lowerKey == "cached_content") && strings.TrimSpace(fmt.Sprint(child)) != "" {
					// Gemini/Vertex explicit-cache storage is billed over time and
					// cannot be reconstructed from this generate request alone.
					return "unsupported_cache_storage_charge"
				}
				if lowerKey == "input_audio" || lowerKey == "audio" {
					return "unsupported_audio_tokens"
				}
				if (lowerKey == "mimetype" || lowerKey == "mime_type") && strings.HasPrefix(strings.ToLower(strings.TrimSpace(fmt.Sprint(child))), "audio/") {
					return "unsupported_audio_tokens"
				}
				if lowerKey == "type" && strings.Contains(strings.ToLower(strings.TrimSpace(fmt.Sprint(child))), "audio") {
					return "unsupported_audio_tokens"
				}
				stack = append(stack, child)
			}
		case []any:
			stack = append(stack, node...)
		}
	}
	return ""
}

func tokenOnlyProviderTool(provider, toolType string) bool {
	if provider != "anthropic" {
		return false
	}
	switch toolType {
	case "web_fetch_20250910", "web_fetch_20260209", "web_fetch_20260309", "web_fetch_20260318":
		return true
	default:
		return false
	}
}

func mergePricingQualifiers(obj map[string]any, usage *UsageObservation) {
	if tier, present, valid := serviceTierFromObject(obj); present {
		if valid {
			usage.ServiceTier = tier
		} else if usage.PricingUnsupportedReason == "" {
			usage.PricingUnsupportedReason = "unsupported_service_tier_shape"
		}
	}
	for _, key := range []string{"trafficType", "traffic_type"} {
		raw, present := obj[key]
		if !present {
			continue
		}
		traffic, ok := raw.(string)
		if !ok || strings.TrimSpace(traffic) == "" {
			usage.PricingUnsupportedReason = "unsupported_traffic_type"
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(traffic)) {
		case "ON_DEMAND":
			usage.ServiceTier = "standard"
		case "PROVISIONED_THROUGHPUT":
			usage.ServiceTier = "provisioned_throughput"
		default:
			usage.ServiceTier = "traffic_type_" + strings.ToLower(strings.TrimSpace(traffic))
		}
	}
	if geo, ok := obj["inference_geo"].(string); ok {
		usage.InferenceGeo = strings.TrimSpace(geo)
	}
	// Server-side tools and grounding are billed outside the token rates in the
	// catalog. Seeing any positive provider counter makes the whole cost
	// explicitly unsupported rather than presenting a token-only subtotal as
	// the exact bill.
	for _, key := range []string{"web_search_requests", "search_queries", "grounding_queries"} {
		if positiveUsageCounter(obj, key, usage) {
			usage.PricingUnsupportedReason = "unsupported_provider_tool_charge"
		}
	}
}

func serviceTierFromObject(obj map[string]any) (tier string, present, valid bool) {
	for _, key := range []string{"service_tier", "serviceTier"} {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		present = true
		switch value := raw.(type) {
		case string:
			tier = strings.TrimSpace(value)
		case map[string]any:
			if typed, ok := value["type"].(string); ok {
				tier = strings.TrimSpace(typed)
			}
		}
		return tier, true, tier != ""
	}
	return "", false, false
}

func mergeProviderOutcomeQualifiers(provider string, obj map[string]any, usage *UsageObservation) {
	if provider != "anthropic" {
		return
	}
	if strings.EqualFold(anthropicStopReason(obj), "refusal") && anthropicOutputBeforeRefusal(obj) == 0 {
		// Anthropic documents refusals before output as unbilled even though a
		// usage object is returned. Preserve tokens, but never manufacture spend.
		usage.PricingUnsupportedReason = "provider_no_charge_refusal"
	}
	if rawUsage, ok := obj["usage"].(map[string]any); ok {
		if iterations, present := rawUsage["iterations"]; present {
			if values, ok := iterations.([]any); !ok || len(values) > 0 {
				usage.PricingUnsupportedReason = "unsupported_model_fallback_pricing"
			}
		}
	}
}

func anthropicStopReason(obj map[string]any) string {
	if stop, _ := obj["stop_reason"].(string); strings.TrimSpace(stop) != "" {
		return strings.TrimSpace(stop)
	}
	if delta, ok := obj["delta"].(map[string]any); ok {
		if stop, _ := delta["stop_reason"].(string); strings.TrimSpace(stop) != "" {
			return strings.TrimSpace(stop)
		}
	}
	return ""
}

func anthropicOutputBeforeRefusal(obj map[string]any) int {
	u, _ := obj["usage"].(map[string]any)
	if u == nil {
		return 0
	}
	if value, ok := nonNegativeInt(u["output_tokens"]); ok {
		return value
	}
	return 0
}

func positiveUsageCounter(obj map[string]any, key string, usage *UsageObservation) bool {
	value, present := obj[key]
	if !present {
		return false
	}
	n, ok := nonNegativeInt(value)
	if !ok {
		usage.Malformed = true
		return false
	}
	return n > 0
}
