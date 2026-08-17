// Package gateway is the byte-safe request lifecycle of the public standalone
// proxy: match → authenticate → inspect → byte-safe transform → upstream →
// meter. It is the same shape as the managed gateway loop but with the
// multi-tenant control plane replaced by three injected seams, so the loop
// carries no cloud coupling:
//
//   - Authenticator    accepts a request and returns its RuntimeMode + optimizer policy
//   - CredentialResolver resolves the upstream provider key (BYOK env or passthrough)
//   - TelemetrySink     records one truthful per-request spend row
//
// record mode is always a pass-through; on any transform problem the original
// bytes are forwarded unchanged. Standalone never claims `verified` savings — the
// sink labels every row `inferred`.
package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/cacheguard"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"github.com/JuliusBrussee/caveman/shared/platform/httpx"
)

// RequestContext is the per-request policy the lifecycle needs. It is the slim,
// single-operator analog of the managed control plane's ProjectContext.
type RequestContext struct {
	// Label tags telemetry rows (e.g. the agent/workflow name); "local" by default.
	Label string
	// AgentSlug attributes the row to the wrapped agent that produced it (from the
	// inbound x-cave-agent header; "unlabeled-agent" when absent). It mirrors the
	// managed gateway's field so the two proxies share one attribution axis. It is a
	// telemetry tag, not policy — the lifecycle sets it after Authenticate.
	AgentSlug string
	// RuntimeMode gates transforms. "record" is always a pass-through.
	RuntimeMode string
	// Optimizers gates which provider-native optimizers may run, keyed by id.
	Optimizers map[string]bool
	// EvalGates records which behavioral optimizers have cleared their eval gate.
	EvalGates map[string]bool
	// ProviderBillingTiers is trusted local config (not a request header). The
	// Gemini Developer API is list-priced only when explicitly set to "paid".
	ProviderBillingTiers map[string]string
}

// Authenticator accepts or rejects an inbound request and returns its context.
// Standalone returns a static single-operator context; a connected build would
// consult its key cache.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (RequestContext, error)
}

// CredentialResolver resolves the upstream provider credential for a request.
// Standalone resolves a BYOK env key by provider, falling back to the inbound
// auth header; the managed gateway reads its ephemeral upstream-key header.
type CredentialResolver interface {
	Resolve(provider string, r *http.Request) providers.Credential
}

// TelemetrySink records one truthful per-request spend row. Standalone writes to
// a local SQLite store; every row's Basis is "inferred".
type TelemetrySink interface {
	Record(RequestRecord)
}

// PayloadSink optionally records raw local trial payloads. It is deliberately
// separate from TelemetrySink so ordinary embedders do not see request bodies.
type PayloadSink interface {
	RecordPayload(label, requestID, traceID string, body []byte)
}

// Compressor is the optional S4 content-compression seam. CompressSegment runs the
// engine on one extracted live-zone content block (byte-safe: returns the original
// bytes with a zero delta on any problem); StoreOriginal records that exact
// original block for CCR recovery and returns its deterministic content-addressed
// handle. Pixel mode still uses StoreOriginal for its full-request recovery record.
// It is defined in primitive terms so the gateway core carries no engine import. A
// nil Compressor means compress mode falls back to a record-mode pass-through —
// embedders wire the real engine or a cloud-backed equivalent.
type Compressor interface {
	CompressSegment(segment []byte) (out []byte, tokensBefore, tokensAfter int)
	StoreOriginal(body []byte) (handle string, err error)
}

// TypedCompressor executes an immutable Cave Build's exact engine transform.
// Locked wrappers must not auto-detect a different compressor than evals approved.
type TypedCompressor interface {
	CompressSegmentType(segment []byte, contentType string) (out []byte, tokensBefore, tokensAfter int)
}

// QueryAwareCompressor is an optional Compressor capability. The gateway passes
// the latest human query from the provider request so structural compressors can
// retain query-relevant rows instead of selecting only by position/statistics.
// Tool results are never used as the query. Implementations remain pure and
// deterministic; empty query must match CompressSegment behavior.
type QueryAwareCompressor interface {
	CompressSegmentQuery(segment []byte, query string) (out []byte, tokensBefore, tokensAfter int)
}

// ToolSchemaStripper is the optional Compressor capability that removes the
// documentation-only JSON-Schema annotation keywords
// ($schema/title/examples/deprecated) from inside a serialized tool catalog's
// schemas. It is a pure byte transform: same input bytes always produce the same
// output bytes, and ok=false means the caller forwards the catalog unchanged. A
// Compressor that does not implement it leaves the tool-schema strip closed no
// matter what the operator configured.
type ToolSchemaStripper interface {
	StripToolSchema(tools []byte) (out []byte, ok bool)
}

// Estimator is the optional observe-only half of Compressor. It measures the
// token reduction a real CompressSegment would achieve WITHOUT storing any CCR
// recovery original (it runs the engine's network-free Simulate path). Record-mode
// observe-estimate uses it so the forwarded bytes stay byte-identical and no
// recovery row is ever written — the estimate is a pure measurement of what
// compression would have cut, recorded as `inferred` and never booked as a saving.
type Estimator interface {
	EstimateSegment(segment []byte) (tokensBefore, tokensAfter int)
}

// QueryAwareEstimator keeps observe-only estimates aligned with live compression.
// Without it, an Estimator retains historical query-agnostic behavior.
type QueryAwareEstimator interface {
	EstimateSegmentQuery(segment []byte, query string) (tokensBefore, tokensAfter int)
}

// Retriever is the optional recovery half of Compressor. Standalone uses it to
// resolve caveman_retrieve calls server-side after S4 compression. Embedders that
// cannot retrieve simply omit it; the proxy then does not advertise its internal
// retrieve tool to the model.
//
// query is the model's optional, plain-language description of the detail it
// needs. handle comes from a <<ccr:...>> marker inside compressed content. When
// query is empty, RetrieveOriginal returns the byte-exact original block. When set,
// the implementation may return only the elided sections most relevant to the
// query (a cheaper, targeted recovery) — but must fall back to the full original
// rather than ever lose detail it cannot rank.
type Retriever interface {
	RetrieveOriginal(handle, query string) ([]byte, error)
}

// PrefixCache is the durable original→replacement map that keeps the upstream
// cache prefix BYTE-STABLE across turns. Live-zone compression rewrites the newest
// message; on the next turn the agent re-sends that same message as its ORIGINAL
// bytes, now below the provider cache floor. Forwarding the original there would
// flip the prefix back to a form the provider never cached and miss the entry the
// previous turn paid to create — at ~60% compression the caller pays more than the
// row claims to have saved. Substituting the stored replacement byte-identically
// removes that divergence entirely.
//
// It is keyed by the content hash of the original block, so it is deterministic
// (same input → same output bytes) and survives process restarts. Every method
// fails OPEN: a miss, an unavailable store, or a write error means the original
// bytes are forwarded and no new replacement is created — the proxy never emits a
// rewrite it could not reproduce on the next turn.
type PrefixCache interface {
	// LookupReplacement returns the exact replacement bytes previously emitted for
	// these original bytes plus the CCR handle they disclose. An evicted or absent
	// entry is a plain miss: the caller forwards the original, which re-syncs the
	// prefix at a one-time cost and stays stable from then on.
	LookupReplacement(scope string, original []byte) (replacement []byte, handle string, ok bool)
	// RememberReplacement durably records original→replacement and returns the
	// AUTHORITATIVE bytes for that original — the caller must forward what comes
	// back, not what it passed in. Storage is first-write-wins so two requests that
	// compressed the same block can never put two different prefixes on the wire.
	RememberReplacement(scope string, original, replacement []byte, handle string) (stored []byte, err error)
}

// PrefixStabilizer is the optional adapter capability that exposes the frozen
// (already-cached) blocks alongside the live zone, so the proxy can substitute a
// replacement it previously emitted for them. Anthropic, OpenAI (and the
// Azure/OpenAI-compatible adapters that reuse its grammar) and Gemini implement it;
// bedrock and vertex do not extract compressible content at all, so they have
// nothing to stabilize and keep the byte-identical passthrough.
type PrefixStabilizer interface {
	ExtractStabilizable(body []byte, meta providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool)
}

// PrefixEvidenceInspector returns ordered exact provider-wire JSON components
// that form frozen prefix. Gateway stores only component hashes and aggregate
// hash. Ordered component hashes prove append-only extension without content.
type PrefixEvidenceInspector interface {
	FrozenPrefixComponents(body []byte, meta providers.RequestMetadata) ([][]byte, bool)
}

// RequestRecord is one proxied call's truthful spend + byte-safety audit row.
type RequestRecord struct {
	Timestamp string
	RequestID string
	TraceID   string
	Label     string
	// Agent framework evidence is caller-supplied content-blind metadata. Gateway
	// validates shape and bounds before recording; local store validates again.
	// These fields join a provider request to one strict Cave Build but never make
	// standalone evidence verified.
	SessionID                    string
	SessionCorrelationBasis      string
	AgentBuildSHA256             string
	EfficiencyPlanSHA256         string
	ContextBill                  string
	TransformTrace               string
	TransformLocation            string
	CacheEpoch                   string
	CachePrefixSHA256            string
	ProviderCachePrefixSHA256    string
	ProviderCacheComponentSHA256 string
	CacheBoundaryKnown           bool
	// CacheBust is set by the observe-only prefix-monotonicity check when this
	// request's frozen prefix did not extend the previous request in the same
	// session (see prefix_monitor.go). It is a diagnostic flag only — it never
	// blocks or modifies traffic and never affects any savings figure.
	CacheBust bool
	// CompressionEligible marks that this request reached the compression path as a
	// candidate (compress mode, recovery-reachable, cache-epoch allowed) regardless
	// of whether any bytes were ultimately saved. It is the denominator behind the
	// requests_eligible_for_compression stats field.
	CompressionEligible bool
	// AgentSlug is the wrapped agent that produced the call (from x-cave-agent;
	// "unlabeled-agent" when absent) — the per-agent attribution dimension.
	AgentSlug                string
	Provider                 string
	Model                    string
	RouteFrom                string
	RouteTo                  string
	Endpoint                 string
	Stream                   bool
	StatusCode               int
	ErrorCode                string
	LatencyMS                int64
	TTFBMS                   int64
	RequestBytes             int
	ResponseBytes            int64
	InputTokens              int
	OutputTokens             int
	CachedInputTokens        int
	CacheCreationInputTokens int
	ReasoningTokens          int
	TotalCostUSD             float64
	SavingsUSD               float64
	// Basis is the savings provenance. Standalone is single-tenant and self-proving,
	// so it is always "inferred" — never "verified" (which the cloud's eval-gated
	// active-mode rollout alone may claim).
	Basis string
	// TokenUsageBasis is independent of savings provenance: provider_complete,
	// provider_partial, provider_malformed, or unavailable.
	TokenUsageBasis          string
	AuthMode                 string
	RuntimeMode              string
	OptimizationIDs          []string
	CacheStatus              string
	RawRequestSHA256         string
	TransformedRequestSHA256 string
	// RequestHashComplete is true only when both hashes cover the complete body
	// accepted by the upstream transport. Streaming transport failures can leave
	// only a prefix; those rows keep hashes empty and mark this false.
	RequestHashComplete bool
	// Compression fields are populated only when compress mode actually shrank the
	// request (zero/empty otherwise). RecoveryHandle contains the CCR block handle
	// list disclosed in in-block <<ccr:...>> markers; CompressionRatio is the
	// inferred live-zone token-reduction fraction (0..1).
	CompressionRatio           float64
	CompressionTokensBefore    int
	CompressionTokensAfter     int
	CompressionTokenCountBasis string
	RecoveryHandle             string
	// WouldSaveTokens is the observe-only estimate: the engine-counted tokens
	// (o200k) that compression WOULD have removed from this request in record mode.
	// It is populated only when observe-estimate is on; it is a measurement, never a
	// booked saving, and never affects SavingsUSD. WouldSaveUSD is the inferred
	// dollar value of those tokens at the model's input rate, non-nil only when the
	// row is list-price eligible (nil otherwise — never a guessed price).
	WouldSaveTokens int
	WouldSaveUSD    *float64
}

// Server is the standalone proxy lifecycle.
type Server struct {
	adapters   []providers.Adapter
	auth       Authenticator
	creds      CredentialResolver
	sink       TelemetrySink
	compressor Compressor
	// prefixCache keeps a compressed message byte-stable on every later turn (see
	// PrefixCache). A nil cache means the proxy cannot maintain a rewrite across
	// turns, which is what the non-PAYG live-zone paths fail closed on.
	prefixCache PrefixCache
	cacheGuard  *cacheguard.Guard
	// prefixMonitor runs the observe-only per-session prefix-monotonicity check
	// (see prefix_monitor.go). It flags cache_bust when a request's frozen prefix
	// does not extend the prior request in the same session.
	prefixMonitor *prefixMonitor
	// cacheEpochGate is the compression GATE for header-less wrap clients: a second,
	// independent prefix monitor keyed on the derived cache epoch. It cannot share
	// prefixMonitor's state (that one is consulted on every request; this one only
	// on compress candidates), and unlike cacheguard it tolerates append-only
	// frozen-prefix growth — see derivedEpochAllows.
	cacheEpochGate *prefixMonitor
	// recoveryViaMCP records that the wrapped agent fulfills caveman_retrieve itself
	// (via the caveman MCP server, sharing the CCR store) — set by `caveman wrap`
	// when it installed that tool. When true, compress mode reshapes streaming and
	// non-streaming requests alike, embeds the recovery handle as a content marker
	// instead of injecting (and never running) the server-side retrieve loop, and
	// claims no savings (retrieves happen off-proxy, so it cannot prove none did).
	recoveryViaMCP bool
	// observeEstimate runs record mode as an observe-only would-have-saved measurement:
	// each live-zone segment is compressed on a COPY to count the tokens compression
	// would remove, the forwarded request stays byte-identical, and no saving is booked.
	observeEstimate bool
	// chatGPTUpstream is the Codex subscription target (test-injectable;
	// DefaultChatGPTUpstream in production). Eligible /responses calls may take
	// live-zone compression before forwarding.
	chatGPTUpstream      string
	subscriptionCompress string
	// toolSchemaStrip selects the tool-schema annotation strip. Only "annotations"
	// enables it; every other value (including the empty default) is off.
	toolSchemaStrip string
	// breakpointPlan selects the cache-breakpoint planner. Only "frontier" enables
	// it; every other value (including the empty default) is off.
	breakpointPlan string
	// ledger is the per-session token account the harm tripwire reasons over, and
	// the freeze registry every lever consults through LeverAllowed. Sessions are
	// identified by the caller's x-cave-session value; a request without one gets
	// no entry and the whole mechanism is inert for it.
	ledger           *sessionLedger
	httpClient       *http.Client
	sessionMarkerKey []byte
	sessionFallback  func(time.Time, string, string) (string, string)
	logger           *slog.Logger
	inflight         atomic.Int64
	// capture is the local body-capture instrument (see capture.go). It is nil
	// unless CAVE_CAPTURE_DIR names a writable directory, and it never affects
	// what is sent, recorded, or claimed.
	capture *bodyCapture
}

// liveZoneCompressionAllowed reports whether subscription- or OAuth-classified
// traffic may take the SAME live-zone compression path PAYG traffic uses. Local
// compression is NOT account-gated: no login, no entitlement, and
// no seat is required, because compression is the free adoption surface. It is on
// by default and the operator turns it off with `subscription_compress: off`; any
// unrecognized value fails closed to off here as well as in the config loader.
//
// Three conditions fail closed, because each one is a way to hand the
// caller a request it cannot recover from or cannot keep paying for:
//
//   - the adapter must expose schema-aware frozen/live zones through
//     PrefixStabilizer. Anthropic uses explicit cache_control breakpoints; OpenAI
//     and Gemini expose only latest-user/latest-tool blocks as live and treat prior
//     matching blocks as substitute-only. Adapters without that contract remain
//     byte-identical passthrough.
//   - recovery must run through the agent's own caveman_retrieve MCP tool. These
//     paths are marker-only — the proxy never injects its server-side retrieve
//     tool into them — so without MCP recovery the elided detail is unreachable.
//   - the proxy must be able to keep the rewrite byte-stable on later turns
//     (prefixStabilized), or turn N+1 flips the prefix back and busts the cache
//     turn N created.
//
// This is a LOCAL-wrap-only capability. The managed gateway's lossless+stealth
// rule for non-PAYG traffic is unchanged. Subscription rows it produces are
// tokens-only: the row's dollar fields stay zero (see record()).
func (s *Server) liveZoneCompressionAllowed(adapter providers.Adapter) bool {
	switch s.subscriptionCompress {
	case "", "live_zone":
	default:
		return false
	}
	if adapter == nil {
		return false
	}
	if !s.recoveryViaMCP {
		return false
	}
	return s.prefixStabilized(adapter)
}

// prefixStabilized reports whether this server + adapter pair can keep a
// compressed message byte-stable on every later turn: the adapter must expose its
// frozen (already-cached) blocks and the server must hold a durable replacement
// cache to substitute them from. Without both, compressing the live zone would
// bust the provider cache on the very next turn, so the non-PAYG paths fail
// closed to passthrough rather than book a saving the caller never receives.
func (s *Server) prefixStabilized(adapter providers.Adapter) bool {
	if s.prefixCache == nil {
		return false
	}
	_, ok := adapter.(PrefixStabilizer)
	return ok
}

// Config injects the three seams plus the upstream HTTP client. A nil HTTPClient
// defaults to a plain client with the standard upstream timeout; the standalone
// binary passes an SSRF-guarded client (see StandaloneHTTPClient).
type Config struct {
	Adapters   []providers.Adapter
	Auth       Authenticator
	Creds      CredentialResolver
	Sink       TelemetrySink
	Compressor Compressor
	// PrefixCache is the durable original→replacement map that keeps a compressed
	// message byte-stable across turns (see PrefixCache). The standalone binary
	// passes its local SQLite store; a nil cache leaves the non-PAYG live-zone
	// paths closed.
	PrefixCache PrefixCache
	// RecoveryViaMCP routes S4 recovery through the agent's own caveman_retrieve MCP
	// tool instead of the proxy's server-side loop (see Server.recoveryViaMCP).
	RecoveryViaMCP bool
	// ObserveEstimate enables record-mode observe-only estimation (see
	// Server.observeEstimate). It requires a Compressor that implements Estimator;
	// without one the record path stays a plain byte-safe pass-through.
	ObserveEstimate bool
	// ChatGPTUpstream overrides the /chatgpt passthrough target (tests only);
	// empty means DefaultChatGPTUpstream.
	ChatGPTUpstream string
	// SubscriptionCompress is the operator off-switch for subscription live-zone
	// compression: empty/"live_zone" allow it, "off" (and any unknown value) fail
	// closed to S0 passthrough. It is the ONLY policy input — there is no account
	// gate.
	SubscriptionCompress string
	// ToolSchemaStrip enables the tool-schema annotation strip when it is exactly
	// "annotations". It is default OFF and additionally requires the same
	// local-wrap conditions live-zone compression requires (see
	// toolSchemaStripAllowed).
	ToolSchemaStrip string
	// BreakpointPlan enables the cache-breakpoint planner when it is exactly
	// "frontier". It is default OFF (see breakpointPlanAllowed).
	BreakpointPlan string
	HTTPClient     *http.Client
	// SessionMarkerKey validates local native-hook correlation markers. Valid
	// markers are stripped before provider inspection and forwarding.
	SessionMarkerKey []byte
	// SessionFallback may return one conservative native-session candidate when
	// host removed signed marker. Returned basis must remain distinct from exact
	// marker correlation; ambiguous cases return empty.
	SessionFallback func(time.Time, string, string) (string, string)
	Logger          *slog.Logger
}

// New constructs a standalone proxy Server.
func New(cfg Config) *Server {
	client := cfg.HTTPClient
	if client == nil {
		timeout := time.Duration(env.Int("CAVE_GATEWAY_UPSTREAM_TIMEOUT_MS", 900000)) * time.Millisecond
		client = &http.Client{Timeout: timeout}
	}
	upstream := cfg.ChatGPTUpstream
	if upstream == "" {
		upstream = DefaultChatGPTUpstream
	}
	if cfg.ToolSchemaStrip == toolSchemaStripMode && cfg.Logger != nil {
		// One startup line, because this lever rewrites the head of the provider
		// cache prefix: the operator should be able to date the one-time cold write
		// to a config change rather than to the provider.
		cfg.Logger.Info("tool-schema annotation strip enabled",
			"mode", toolSchemaStripMode, "strip_version", toolSchemaStripVersion)
	}
	if cfg.BreakpointPlan == breakpointPlanModeFrontier && cfg.Logger != nil {
		// One startup line, for the same reason the strip has one: the planner moves
		// where the provider cache boundary sits, so the operator should be able to
		// date that change to a config change rather than to the provider.
		cfg.Logger.Info("cache-breakpoint planner enabled", "mode", breakpointPlanModeFrontier)
	}
	return &Server{
		adapters:             cfg.Adapters,
		auth:                 cfg.Auth,
		creds:                cfg.Creds,
		sink:                 cfg.Sink,
		compressor:           cfg.Compressor,
		prefixCache:          cfg.PrefixCache,
		cacheGuard:           cacheguard.New(),
		prefixMonitor:        newPrefixMonitor(),
		cacheEpochGate:       newPrefixMonitor(),
		recoveryViaMCP:       cfg.RecoveryViaMCP,
		observeEstimate:      cfg.ObserveEstimate,
		chatGPTUpstream:      strings.TrimSuffix(upstream, "/"),
		subscriptionCompress: cfg.SubscriptionCompress,
		toolSchemaStrip:      cfg.ToolSchemaStrip,
		breakpointPlan:       cfg.BreakpointPlan,
		ledger:               newSessionLedger(),
		httpClient:           client,
		sessionMarkerKey:     append([]byte(nil), cfg.SessionMarkerKey...),
		sessionFallback:      cfg.SessionFallback,
		logger:               cfg.Logger,
		capture:              newBodyCapture(os.Getenv("CAVE_CAPTURE_DIR"), cfg.Logger),
	}
}

// Handler returns the standalone HTTP handler: health, metrics, and the proxy
// catch-all. Unlike the managed gateway it serves no SDK/OTLP endpoints — the
// standalone proxy is a pure base-URL swap.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.health)
	mux.HandleFunc("GET /health/ready", s.health)
	mux.HandleFunc("GET /metrics", s.metrics)
	// ChatGPT-login Codex: OAuth-preserving forward with OpenAI Responses
	// live-zone compression and exact-original fallback.
	mux.HandleFunc("/chatgpt/", s.chatgpt)
	mux.HandleFunc("/", s.proxy)
	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"service":  "caveman-proxy",
		"schema":   "caveman.proxy.health.v1",
		"billing":  "byok",
		"adapters": len(s.adapters),
	})
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte("cave_proxy_inflight_requests "))
	_, _ = w.Write([]byte(itoa(s.inflight.Load())))
	_, _ = w.Write([]byte("\n"))
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
