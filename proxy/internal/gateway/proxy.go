package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/nativeruntime"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openaicompat"
	"github.com/JuliusBrussee/caveman/shared/platform/cacheguard"
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"github.com/JuliusBrussee/caveman/shared/platform/httpx"
	"github.com/JuliusBrussee/caveman/shared/platform/id"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	if !normalizeAgentPath(r) {
		httpx.Error(w, r, http.StatusNotFound, "cave_route_not_found", "Proxy path is not recognized.")
		return
	}
	start := time.Now()
	requestID := id.NewUUIDv7()
	traceID := traceIDFrom(r)
	w.Header().Set("x-cave-request-id", requestID)
	w.Header().Set("x-cave-trace-id", traceID)

	adapter := s.matchAdapter(r)
	if adapter == nil {
		// fail-closed routing: an unrecognized path is a 404, never a blind pass-through.
		httpx.Error(w, r, http.StatusNotFound, "cave_route_not_found", "Proxy path is not recognized.")
		return
	}
	rc, err := s.auth.Authenticate(r.Context(), r)
	if err != nil {
		httpx.Error(w, r, http.StatusUnauthorized, "cave_unauthorized", "Request rejected by the proxy authenticator.")
		return
	}
	credential := s.creds.Resolve(adapter.Name(), r)
	authMode := ClassifyResolvedAuthMode(r.Header, credential)
	// Local compression is not account-gated, so both non-PAYG
	// classifications reach the live zone on the same purely technical conditions:
	// the operator off-switch, an adapter with cache-floor reasoning, and the MCP
	// recovery + byte-stable prefix machinery that makes the rewrite recoverable
	// and maintainable.
	nonPAYGLiveZone := (authMode == AuthModeSubscription || authMode == AuthModeOAuth) && s.liveZoneCompressionAllowed(adapter)
	subscriptionPassthrough := authMode == AuthModeSubscription && !nonPAYGLiveZone
	// Attribute the row to the wrapped agent (e.g. `caveman wrap opencode` sets
	// x-cave-agent: opencode). Default to "unlabeled-agent" so the dimension is
	// always present. Telemetry-only: it never gates routing or transforms.
	rc.AgentSlug = labelOrDefault(r.Header.Get("x-cave-agent"), "unlabeled-agent")

	maxBytes := int64(env.Int("CAVE_MAX_REQUEST_BYTES", 33554432))
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	_ = r.Body.Close()
	if err != nil || int64(len(body)) > maxBytes {
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, "cave_request_too_large", "Request body exceeds the proxy limit.")
		return
	}
	body, correlatedSessionID, _ := nativeruntime.StripSessionMarkers(body, s.sessionMarkerKey)
	if strings.HasPrefix(labelOrDefault(rc.Label, "local"), "trial:") {
		if payloads, ok := s.sink.(PayloadSink); ok {
			payloads.RecordPayload(rc.Label, requestID, traceID, body)
		}
	}
	rawHash := sha256.Sum256(body)
	evidence := requestEvidenceFromHeaders(r.Header)
	if correlatedSessionID != "" {
		evidence.SessionID = correlatedSessionID
		evidence.SessionCorrelationBasis = "signed_marker"
	} else if evidence.SessionID != "" {
		evidence.SessionCorrelationBasis = "explicit_header"
	}

	headersForInspect := r.Header.Clone()
	headersForInspect.Set("x-cave-route-path", r.URL.Path)
	meta, err := adapter.InspectRequest(r.Context(), bytes.NewReader(body), headersForInspect)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "cave_provider_request_invalid", "Provider request metadata could not be inspected.")
		return
	}
	meta.BillingTier = rc.ProviderBillingTiers[meta.Provider]
	if evidence.SessionID == "" && s.sessionFallback != nil {
		if sessionID, basis := s.sessionFallback(time.Now(), meta.Provider, meta.Model); sessionID != "" && (basis == "unique_recent_session" || basis == "unique_recent_time_model") {
			evidence.SessionID = sessionID
			evidence.SessionCorrelationBasis = basis
		}
	}
	// Record the real request path rather than the adapter's default provider tag.
	meta.Endpoint = r.URL.Path
	// The caller's session identity, already shape-bounded by requestEvidenceFromHeaders.
	// It reaches adapters as content-blind routing input only (see RequestMetadata).
	meta.SessionID = evidence.SessionID

	// byte-safe transform. record mode never transforms. On ANY transform error we
	// forward the ORIGINAL bytes unchanged (fail-open) rather than failing the
	// request — the operator's traffic must never break because an optimizer hint
	// could not be applied. compress mode (S4) is the one path that intentionally
	// reshapes model-visible bytes; it is opt-in, recoverable via CCR, and disclosed.
	transform := providers.TransformResult{Body: body, OptimizerIDs: []string{}}
	var comp *compressionOutcome
	// toolSchemaHandle is the CCR handle of the original tool catalog, set only
	// when the tool-schema annotation strip actually rewrote it.
	toolSchemaHandle := ""
	// breakpointPlanned records that the cache-breakpoint planner placed provider
	// cache metadata on this request. The session ledger needs it to know which
	// levers were active when it later evaluates the harm tripwire.
	breakpointPlanned := false
	var estimate *estimateOutcome
	var estimateWG sync.WaitGroup
	retrieveInjected := false
	compressionEligible := false
	effectiveRuntimeMode := rc.RuntimeMode
	if strings.TrimSpace(r.Header.Get("x-cave-transforms")) == "caveman.pass-through.v1" {
		// Framework callers use this request-wide opt-out when their result contract
		// cannot observe or recover transformed bytes. It suppresses compression,
		// pixel, and provider-native transforms while preserving configured mode in
		// response/telemetry. Record behavior may still simulate an estimate on copies.
		effectiveRuntimeMode = "record"
	}
	switch effectiveRuntimeMode {
	case "record":
		// always a pure pass-through. When observe-estimate is on, measure — on
		// COPIES, storing nothing — how many tokens compression WOULD have removed.
		// The forwarded body stays byte-identical; any estimate error is swallowed.
		//
		// Run the estimate CONCURRENTLY with the upstream round-trip so its
		// tokenizer+Simulate cost never lands on time-to-first-byte. This is race-free:
		// the goroutine only READS body (segments are copied before the engine touches
		// them) — the same body the upstream reader consumes read-only — and meta is
		// snapshotted by value here, before the main path reassigns it via
		// ApplyResolvedPricingRoute below. `estimate` is written by exactly one
		// goroutine and read only after estimateWG.Wait() (which establishes the
		// happens-before), so every record() consumer sees a fully-computed value.
		if s.observeEstimate {
			estimateWG.Add(1)
			go func(m providers.RequestMetadata) {
				defer estimateWG.Done()
				estimate = s.estimateRequest(adapter, body, m)
			}(meta)
		}
	case "compress":
		if s.compressor == nil {
			break
		}
		lockedRoutes, compiledPlanAllowed := compiledPlanRoutes(r.Header)
		if !compiledPlanAllowed {
			break
		}
		// Subscription-classified traffic falls back to S0 passthrough whenever the
		// live-zone conditions do not hold (operator off-switch, no schema-aware
		// prefix stabilizer, no MCP recovery, no durable prefix cache) — the path
		// prior Claude Code sessions proved safe after byte-modified requests drew
		// opaque 429s. When they DO hold it takes the very same live-zone path PAYG
		// traffic uses — marker-only, cache-floor aware — and its row is recorded
		// tokens-only, never in dollars.
		if subscriptionPassthrough {
			break
		}
		serverRetrieveAllowed := authMode == AuthModePAYG && !s.recoveryViaMCP && !meta.Stream && !hasRetrieveTool(body) && serverRetrieveSupported(meta.Provider, meta.Endpoint)
		if serverRetrieveAllowed {
			if _, canRetrieve := s.compressor.(Retriever); !canRetrieve {
				serverRetrieveAllowed = false
			}
		}
		// Marker-only compression needs a recovery path the caller can actually reach.
		// PAYG keeps its pre-existing MCP-recovery rule; subscription and OAuth go
		// exclusively through the live-zone predicate above (which itself requires MCP
		// recovery), so neither can ever compress with no way back to the elided bytes.
		markerOnlyAllowed := (s.recoveryViaMCP && authMode != AuthModeOAuth && authMode != AuthModeSubscription) || nonPAYGLiveZone
		if (markerOnlyAllowed || serverRetrieveAllowed) && s.cacheEpochAllows(r, adapter, meta, body, evidence.SessionID) {
			// The request reached the compression path as a candidate. It is eligible
			// whether or not compressRequest ultimately shrinks any bytes — the
			// requests_eligible_for_compression denominator counts candidates, not wins.
			compressionEligible = true
			comp = s.compressRequest(adapter, body, meta, &transform, requestID, lockedRoutes)
		}
		if serverRetrieveAllowed {
			if injected, ok := injectRetrieveTool(meta.Provider, meta.Endpoint, transform.Body); ok {
				transform.Body = injected
				retrieveInjected = true
				if comp != nil {
					comp.bookSavings = true
				}
			} else {
				if comp != nil {
					comp = nil
					transform = providers.TransformResult{Body: body, OptimizerIDs: []string{}}
				}
			}
		}
		// The tool-schema annotation strip is its own path, not a rewritable block:
		// the tool catalog is frozen prefix that extractRewritable deliberately
		// never yields, and compressRequest's `!block.live` guard must keep
		// protecting that prefix from the general rewrite loop. See
		// stripToolSchema for the determinism invariant that makes a frozen-prefix
		// rewrite admissible here. It runs last so its byte offsets are computed on
		// the bytes actually going upstream, and it is skipped under a compiled
		// Cave Build, whose transform set is locked to what evals approved.
		if len(lockedRoutes) == 0 && s.toolSchemaStripAllowed(adapter, evidence.SessionID) && s.cacheEpochAllows(r, adapter, meta, transform.Body, evidence.SessionID) {
			if stripped, handle, ok := s.stripToolSchema(transform.Body, meta, requestID); ok {
				transform.Body = stripped
				transform.OptimizerIDs = append(transform.OptimizerIDs, toolSchemaStripOptimizerID)
				toolSchemaHandle = handle
			}
		}
	case "pixel":
		if s.compressor == nil {
			break
		}
		if authMode == AuthModeSubscription {
			break
		}
		comp = s.pixelRequest(adapter, body, meta, &transform, requestID)
	default:
		if authMode != AuthModeSubscription {
			t, terr := adapter.ApplyProviderNativeTransforms(r.Context(), bytes.NewReader(body), meta, providers.TransformPolicy{
				RuntimeMode: rc.RuntimeMode,
				AuthMode:    string(authMode),
				Optimizers:  rc.Optimizers,
				EvalGates:   rc.EvalGates,
			})
			if terr != nil {
				if s.logger != nil {
					s.logger.Warn("transform failed; forwarding original bytes unchanged", "error", redact.Error(terr), "request_id", requestID)
				}
			} else {
				transform = t
			}
		}
		// The cache-breakpoint planner runs on the bytes actually going upstream, so
		// its placement is computed after any provider-native transform above.
		//
		// Unlike that transform it is NOT skipped for subscription traffic, because it
		// adds provider metadata rather than model-visible bytes. Both of its live
		// arms are payg-gated today, so subscription traffic gets nothing from it; the
		// reach is kept for the reworked lookback guard, whose entire purpose is
		// harnesses that already place cache_control. record mode never reaches this
		// branch at all, so a pass-through request is untouched by construction.
		if s.breakpointPlanAllowed(evidence.SessionID) {
			if planned, ok := s.planBreakpoints(adapter, transform.Body, meta, authMode == AuthModePAYG); ok {
				transform.Body = planned
				transform.OptimizerIDs = append(transform.OptimizerIDs, breakpointPlanOptimizerID)
				breakpointPlanned = true
			}
		}
	}
	transformedHash := sha256.Sum256(transform.Body)
	providerCachePrefixSHA256, providerCacheComponentSHA256, cacheBoundaryKnown := providerPrefixEvidence(adapter, transform.Body, meta)

	// Observe-only prefix-monotonicity check (issue #133): compare this request's
	// frozen-prefix component hashes against the previous request in the same
	// session and flag cache_bust if the new prefix does not extend the prior one.
	// It never blocks or modifies traffic — the flag is persisted and a Warn names
	// the first diverging component index. Evaluated once here on the bytes we send
	// upstream; the byte-safe retry below keeps this verdict rather than re-running
	// the stateful monitor for the same request.
	cacheBust, divergingComponentIndex := s.prefixMonitor.observe(evidence.SessionID, providerCacheComponentSHA256)
	if cacheBust && s.logger != nil {
		s.logger.Warn("session frozen-cache prefix did not extend prior request; possible cache bust",
			"request_id", requestID, "session_id", evidence.SessionID,
			"diverging_component_index", divergingComponentIndex)
	}

	upstreamURL, err := adapter.ResolveUpstreamURL(r.Context(), r, providers.RouteContext{})
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unavailable", "Upstream route could not be resolved.")
		return
	}
	meta = providers.ApplyResolvedPricingRoute(meta, upstreamURL)
	authContext := providers.WithRequestPayloadHash(r.Context(), transform.Body)
	upstreamHeaders, err := adapter.SanitizeAndMapHeaders(authContext, r, credential, upstreamURL)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "cave_header_mapping_failed", "Headers could not be mapped safely.")
		return
	}
	if authMode != AuthModePAYG && r.UserAgent() != "" {
		upstreamHeaders.Set("user-agent", r.UserAgent())
	}
	s.applyUpstreamAuthFallback(adapter.Name(), credential, upstreamHeaders)
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL.String(), bytes.NewReader(transform.Body))
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unavailable", "Upstream request could not be created.")
		return
	}
	req.Header = upstreamHeaders

	s.inflight.Add(1)
	// Capture both sides of the transform before the send (see capture.go). Off
	// unless CAVE_CAPTURE_DIR is set; nil-guarded inside record, and queued rather
	// than written here so the inflight window carries no disk write.
	s.capture.record(captureMeta{
		RequestID:   requestID,
		Provider:    meta.Provider,
		Endpoint:    meta.Endpoint,
		RuntimeMode: effectiveRuntimeMode,
		Optimizers:  strings.Join(transform.OptimizerIDs, ","),
	}, wholeBody(body), wholeBody(transform.Body))

	resp, err := s.httpClient.Do(req)
	s.inflight.Add(-1)
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unavailable", "Upstream provider unavailable.")
		estimateWG.Wait() // join the observe estimate before record() reads it
		s.record(start, 0, requestID, traceID, rc, meta, authMode, http.StatusBadGateway, 0, len(body), rawHash, transformedHash, "cave_upstream_unavailable", transform.OptimizerIDs, providers.UsageObservation{CacheStatus: "unknown"}, comp, toolSchemaHandle, false, estimate, evidence, providerCachePrefixSHA256, providerCacheComponentSHA256, cacheBoundaryKnown, cacheBust, compressionEligible)
		return
	}
	// byte-safe fail-open: if the upstream rejects a request whose bytes we
	// modified, retry ONCE with the original bytes before surfacing the error —
	// a transform must never break traffic that would have succeeded untouched.
	// (Measured 2026-07-07: Anthropic answers subscription-OAuth requests whose
	// first system block changed with an opaque 429; any future fingerprint check
	// lands here too.) The retry claims no optimization and books no savings.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && !bytes.Equal(transform.Body, body) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if s.logger != nil {
			s.logger.Warn("upstream rejected transformed request; retrying with original bytes", "status", resp.StatusCode, "request_id", requestID)
		}
		retryReq, rerr := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL.String(), bytes.NewReader(body))
		if rerr != nil {
			httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unavailable", "Upstream request could not be created.")
			return
		}
		retryAuthContext := providers.WithRequestPayloadHash(r.Context(), body)
		retryHeaders, rerr := adapter.SanitizeAndMapHeaders(retryAuthContext, r, credential, upstreamURL)
		if rerr != nil {
			httpx.Error(w, r, http.StatusBadRequest, "cave_header_mapping_failed", "Headers could not be mapped safely.")
			return
		}
		if authMode != AuthModePAYG && r.UserAgent() != "" {
			retryHeaders.Set("user-agent", r.UserAgent())
		}
		s.applyUpstreamAuthFallback(adapter.Name(), credential, retryHeaders)
		retryReq.Header = retryHeaders
		s.inflight.Add(1)
		retryResp, derr := s.httpClient.Do(retryReq)
		s.inflight.Add(-1)
		if derr != nil {
			httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unavailable", "Upstream provider unavailable.")
			estimateWG.Wait() // join the observe estimate; passed uniformly (zeroed at Record on this failed status)
			s.record(start, 0, requestID, traceID, rc, meta, authMode, http.StatusBadGateway, 0, len(body), rawHash, rawHash, "cave_upstream_unavailable", []string{}, providers.UsageObservation{CacheStatus: "unknown"}, nil, "", false, estimate, evidence, providerCachePrefixSHA256, providerCacheComponentSHA256, cacheBoundaryKnown, cacheBust, compressionEligible)
			return
		}
		resp = retryResp
		transform = providers.TransformResult{Body: body, OptimizerIDs: []string{}}
		transformedHash = rawHash
		providerCachePrefixSHA256, providerCacheComponentSHA256, cacheBoundaryKnown = providerPrefixEvidence(adapter, body, meta)
		comp = nil
		toolSchemaHandle = ""
		breakpointPlanned = false
		retrieveInjected = false
		// The ORIGINAL bytes served this request, so the capture written before the
		// first attempt describes bytes the upstream rejected. Write a second record
		// for the same request id rather than leaving the first one to be read as
		// what was answered — a capture that is confidently wrong is the exact
		// failure this instrument exists to remove.
		s.capture.record(captureMeta{
			RequestID:     requestID,
			Provider:      meta.Provider,
			Endpoint:      meta.Endpoint,
			RuntimeMode:   effectiveRuntimeMode,
			RetryOriginal: true,
		}, wholeBody(body), wholeBody(body))
	}
	var retrieveCalls []providers.UsageObservation
	var retrieved bool
	var replayedOriginal bool
	if retrieveInjected {
		resp, retrieveCalls, retrieved, replayedOriginal = s.runRetrieveLoop(r.Context(), s.httpClient, upstreamURL, upstreamHeaders, transform.Body, body, comp.recoveryHandles(), resp, adapter, meta.Provider, meta.Endpoint, requestID)
	}
	if replayedOriginal {
		transform = providers.TransformResult{Body: body, OptimizerIDs: []string{}}
		transformedHash = rawHash
		providerCachePrefixSHA256, providerCacheComponentSHA256, cacheBoundaryKnown = providerPrefixEvidence(adapter, body, meta)
		comp = nil
		toolSchemaHandle = ""
		breakpointPlanned = false
		retrieveInjected = false
		s.capture.record(captureMeta{
			RequestID:     requestID,
			Provider:      meta.Provider,
			Endpoint:      meta.Endpoint,
			RuntimeMode:   effectiveRuntimeMode,
			RetryOriginal: true,
		}, wholeBody(body), wholeBody(body))
	}
	defer resp.Body.Close()

	copySafeResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("x-cave-project", rc.Label)
	w.Header().Set("x-cave-mode", rc.RuntimeMode)
	if len(transform.OptimizerIDs) == 0 {
		w.Header().Set("x-cave-optimization", "none")
	} else {
		w.Header().Set("x-cave-optimization", strings.Join(transform.OptimizerIDs, ","))
	}
	// per-request disclosure of S4 compression: ratio, the CCR recovery handle for
	// the original request, and the inferred token counts. Set only when compress
	// mode actually shrank the request.
	if comp != nil {
		w.Header().Set("x-caveman-compression-ratio", strconv.FormatFloat(comp.ratio, 'f', 4, 64))
		w.Header().Set("x-caveman-recovery-handle", comp.handle)
		w.Header().Set("x-caveman-tokens-before", strconv.Itoa(comp.before))
		w.Header().Set("x-caveman-tokens-after", strconv.Itoa(comp.after))
		w.Header().Set("x-caveman-token-count-basis", "estimated_engine_o200k")
	}
	// per-request disclosure of the tool-schema strip: which strip ran and the CCR
	// handle of the exact original catalog. It carries no token figure — the strip
	// claims no saving here; the replay grid prices it.
	if toolSchemaHandle != "" {
		w.Header().Set("x-caveman-toolschema-strip", toolSchemaStripMode)
		w.Header().Set("x-caveman-toolschema-recovery-handle", toolSchemaHandle)
	}
	// per-request disclosure of the harm tripwire: which levers this session has
	// lost. It appears from the request AFTER the one that tripped, because the
	// usage that trips it is only known once the response is already streaming.
	if tripwire := s.tripwireDisclosure(evidence.SessionID); tripwire != "" {
		w.Header().Set("x-caveman-tripwire", tripwire)
	}
	w.WriteHeader(resp.StatusCode)

	counter := &countingWriter{w: w}
	usageScanner := adapter.NewUsageScanner(resp.Header)
	_, copyErr := copyFlush(counter, io.TeeReader(resp.Body, usageScanner))
	ttfb := time.Since(start).Milliseconds()
	if !counter.firstByteAt.IsZero() {
		ttfb = counter.firstByteAt.Sub(start).Milliseconds()
	}
	if copyErr != nil && s.logger != nil {
		s.logger.Warn("client stream copy failed", "error", redact.Error(copyErr), "request_id", requestID)
	}
	errCode := ""
	if resp.StatusCode >= 400 {
		errCode = fmt.Sprintf("provider_%d", resp.StatusCode)
	}
	finalUsage := usageScanner.Usage()
	// The session ledger sees the provider's own numbers for the upstream call the
	// levers actually shaped — not the retrieve-loop total below, whose extra calls
	// carry their own prefixes and would blur the cache-creation signal the harm
	// tripwire reads.
	var activeLevers []lever
	if toolSchemaHandle != "" {
		activeLevers = append(activeLevers, leverToolSchemaStrip)
	}
	if breakpointPlanned {
		activeLevers = append(activeLevers, leverBreakpointPlan)
	}
	s.observeSession(evidence.SessionID, activeLevers, finalUsage, requestID)
	combinedUsage := finalUsage
	for _, callUsage := range retrieveCalls {
		combinedUsage = addUsage(combinedUsage, callUsage)
	}
	if len(retrieveCalls) > 0 {
		combinedUsage.CallObservations = append(append([]providers.UsageObservation{}, retrieveCalls...), finalUsage)
	}
	estimateWG.Wait() // join the observe estimate; overlapped the upstream round-trip + response stream
	s.record(start, ttfb, requestID, traceID, rc, meta, authMode, resp.StatusCode, counter.n, len(body), rawHash, transformedHash, errCode, transform.OptimizerIDs, combinedUsage, comp, toolSchemaHandle, retrieved, estimate, evidence, providerCachePrefixSHA256, providerCacheComponentSHA256, cacheBoundaryKnown, cacheBust, compressionEligible)
}

func providerPrefixEvidence(adapter providers.Adapter, body []byte, meta providers.RequestMetadata) (string, string, bool) {
	inspector, ok := adapter.(PrefixEvidenceInspector)
	if !ok {
		return "", "", false
	}
	components, ok := inspector.FrozenPrefixComponents(body, meta)
	if !ok || len(components) == 0 || len(components) > 1_024 {
		return "", "", false
	}
	whole := sha256.New()
	hashes := make([]string, 0, len(components))
	for _, component := range components {
		if len(component) > 32<<20 {
			return "", "", false
		}
		digest := sha256.Sum256(component)
		hashes = append(hashes, hex.EncodeToString(digest[:]))
		_, _ = whole.Write(component)
	}
	return hex.EncodeToString(whole.Sum(nil)), strings.Join(hashes, ","), true
}

type requestEvidence struct {
	SessionID               string
	SessionCorrelationBasis string
	AgentBuildSHA256        string
	EfficiencyPlanSHA256    string
	ContextBill             string
	TransformTrace          string
	TransformLocation       string
	CacheEpoch              string
	CachePrefixSHA256       string
}

func requestEvidenceFromHeaders(headers http.Header) requestEvidence {
	build := strings.TrimSpace(headers.Get("x-cave-agent-build"))
	plan := strings.TrimSpace(headers.Get("x-cave-efficiency-plan"))
	if !validGatewayDigest(build) || !validGatewayDigest(plan) {
		build, plan = "", ""
	}
	prefix := strings.TrimSpace(headers.Get("x-cave-cache-prefix-sha256"))
	if !validGatewayDigest(prefix) {
		prefix = ""
	}
	location := strings.TrimSpace(headers.Get("x-cave-transform-location"))
	if location != "local" && location != "gateway" {
		location = ""
	}
	return requestEvidence{
		SessionID:            boundedEvidenceToken(headers.Get("x-cave-session"), 256),
		AgentBuildSHA256:     build,
		EfficiencyPlanSHA256: plan,
		ContextBill:          boundedEvidenceValue(headers.Get("x-cave-context-bill"), 2_048),
		TransformTrace:       boundedEvidenceValue(headers.Get("x-cave-transform-trace"), 8_192),
		TransformLocation:    location,
		CacheEpoch:           boundedEvidenceToken(headers.Get("x-cave-cache-epoch"), 512),
		CachePrefixSHA256:    prefix,
	}
}

func boundedEvidenceToken(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit {
		return ""
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && !strings.ContainsRune("._:-", char) {
			return ""
		}
	}
	return value
}

func boundedEvidenceValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit {
		return ""
	}
	for _, char := range value {
		if char < 0x20 || char > 0x7e {
			return ""
		}
	}
	return value
}

// compiledPlanAllowsCompression keeps entitlement defaults from overriding an
// immutable Claude Cave Build. No build header means legacy/unlocked behavior.
// A locked baseline explicitly selects pass-through; unknown or partial lock
// identity also fails closed to original bytes.
func compiledPlanAllowsCompression(headers http.Header) bool {
	_, allowed := compiledPlanRoutes(headers)
	return allowed
}

func compiledPlanCompression(headers http.Header) (contentType, transformID string, allowed bool) {
	routes, allowed := compiledPlanRoutes(headers)
	if !allowed || len(routes) != 1 {
		return "", "", false
	}
	return routes[0].ContentType, routes[0].TransformID, true
}

type compiledRoute struct {
	SegmentID   string `json:"segment_id,omitempty"`
	SegmentKind string `json:"segment_kind"`
	TransformID string `json:"transform_id"`
	ContentType string `json:"-"`
}

func compiledPlanRoutes(headers http.Header) ([]compiledRoute, bool) {
	build := strings.TrimSpace(headers.Get("x-cave-agent-build"))
	plan := strings.TrimSpace(headers.Get("x-cave-efficiency-plan"))
	transforms := strings.TrimSpace(headers.Get("x-cave-transforms"))
	if strings.TrimSpace(headers.Get("x-cave-transform-location")) == "local" {
		// Pi runtime already applied plan bytes. Gateway is transport + metering;
		// a second entitlement-conditioned transform would invalidate baseline
		// and selected-plan evidence.
		return nil, false
	}
	// Unlocked framework callers may explicitly require byte-identical transport.
	// This opt-out carries no build claim and only removes capability from the
	// caller's own request, so it is safe without lock identity.
	if transforms == "caveman.pass-through.v1" {
		return nil, false
	}
	if build == "" && plan == "" {
		return nil, true
	}
	if !validGatewayDigest(build) || !validGatewayDigest(plan) || transforms == "" {
		return nil, false
	}
	parts := strings.Split(transforms, ",")
	var routes []compiledRoute
	encodedRoutes := strings.TrimSpace(headers.Get("x-cave-transform-routes"))
	if encodedRoutes == "" {
		if len(parts) != 1 {
			return nil, false
		}
		routes = []compiledRoute{{SegmentKind: "history", TransformID: strings.TrimSpace(parts[0])}}
	} else {
		raw, err := base64.RawURLEncoding.DecodeString(encodedRoutes)
		if err != nil || len(raw) > 16*1024 || json.Unmarshal(raw, &routes) != nil ||
			len(routes) == 0 || len(routes) > 32 || len(routes) != len(parts) {
			return nil, false
		}
	}
	for i := range routes {
		routes[i].TransformID = strings.TrimSpace(routes[i].TransformID)
		if routes[i].TransformID != strings.TrimSpace(parts[i]) ||
			!validCompiledSegmentKind(routes[i].SegmentKind) ||
			(routes[i].SegmentID != "" && len(routes[i].SegmentID) > 256) {
			return nil, false
		}
		contentType, ok := compiledContentType(routes[i].TransformID)
		if !ok {
			return nil, false
		}
		routes[i].ContentType = contentType
	}
	return routes, true
}

func compiledContentType(transformID string) (string, bool) {
	const prefix, suffix = "caveman.engine.", ".v1"
	if !strings.HasPrefix(transformID, prefix) || !strings.HasSuffix(transformID, suffix) {
		return "", false
	}
	contentType := strings.TrimSuffix(strings.TrimPrefix(transformID, prefix), suffix)
	switch contentType {
	case "a11y", "code", "config", "diff", "html", "json", "log", "repetition",
		"search-result", "tabular", "terminal", "text", "toolschema", "toon":
		return contentType, true
	default:
		return "", false
	}
}

func validCompiledSegmentKind(kind string) bool {
	switch kind {
	case "instruction", "user_intent", "tool_schema", "skill", "memory", "history",
		"tool_result", "artifact", "error", "output_contract":
		return true
	default:
		return false
	}
}

func validGatewayDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// cacheEpochAllows binds framework-frozen prefix identity to gateway transform
// authorization without putting prompt bytes in headers or telemetry. Legacy
// callers that send neither header keep existing provider-adapter enforcement;
// a partial/invalid framework declaration fails closed to pass-through.
func (s *Server) cacheEpochAllows(r *http.Request, adapter providers.Adapter, meta providers.RequestMetadata, body []byte, sessionID string) bool {
	epoch := strings.TrimSpace(r.Header.Get("x-cave-cache-epoch"))
	digest := strings.TrimSpace(r.Header.Get("x-cave-cache-prefix-sha256"))
	if epoch == "" && digest == "" {
		// Header-less wrap clients (Claude Code, Codex, Gemini CLI) never declare a
		// cache epoch or prefix digest, so without a derived path the gate never runs
		// for the wrap's actual users (issue #133). It CANNOT reuse cacheguard.Inspect:
		// that compares whole-prefix digests for equality, but the Anthropic frozen
		// prefix grows every turn as the cache floor advances (content_compress.go), so
		// whole-prefix equality reads legitimate append-only growth as drift and would
		// skip compression from turn 2 onward — a coverage regression for the exact
		// clients this path serves. Instead gate on the SAME append-only component
		// check SLICE 1 uses (derivedEpochAllows).
		return s.derivedEpochAllows(adapter, meta, body, sessionID)
	}
	if epoch == "" || digest == "" || s.cacheGuard == nil {
		return false
	}
	result, err := s.cacheGuard.Inspect(cacheguard.Input{
		EpochID:       epoch + ":" + meta.Provider + ":" + meta.Endpoint,
		PrefixSHA256:  digest,
		BoundaryKnown: true,
		AdapterKnown:  s.prefixStabilized(adapter),
	})
	if err != nil || result.Decision == cacheguard.DecisionPassThrough {
		if s.logger != nil {
			s.logger.Warn("cache epoch rejected transformed request; forwarding original bytes",
				"warnings", result.Warnings)
		}
		return false
	}
	return true
}

// derivedEpochAllows is the compression gate for header-less wrap clients. It reuses
// SLICE 1's append-only component comparison (prefix_monitor) rather than
// cacheguard's whole-prefix equality, so it is extension-tolerant: a request whose
// frozen prefix APPEND-ONLY EXTENDS the stored one is allowed AND re-anchors the
// stored prefix to the new (longer) one, so a growing Claude Code session keeps
// compressing every turn instead of getting stuck on turn 1. Only a genuine
// divergence — a frozen component changed, dropped, or reordered (the same
// condition SLICE 1 flags as cache_bust) — is drift: compression is skipped and the
// ORIGINAL bytes are forwarded (byte-safe, blocks nothing, books nothing). The gate
// re-anchors on divergence too, so a real prefix change resyncs the next turn rather
// than stalling compression for the rest of the session.
//
// With no correlated session, or an adapter that exposes no frozen prefix, it keeps
// the legacy behavior of leaving provider-adapter enforcement in charge (allow).
func (s *Server) derivedEpochAllows(adapter providers.Adapter, meta providers.RequestMetadata, body []byte, sessionID string) bool {
	if sessionID == "" {
		return true
	}
	_, components, boundaryKnown := providerPrefixEvidence(adapter, body, meta)
	if components == "" || !boundaryKnown {
		return true
	}
	epochID := "wrap:" + sessionID + ":" + meta.Provider + ":" + meta.Endpoint
	bust, divergingIndex := s.cacheEpochGate.observe(epochID, components)
	if bust {
		if s.logger != nil {
			s.logger.Warn("derived cache epoch prefix diverged; forwarding original bytes",
				"provider", meta.Provider, "endpoint", meta.Endpoint,
				"diverging_component_index", divergingIndex)
		}
		return false
	}
	return true
}

// estimateOutcome carries a record-mode observe-only measurement: the engine's
// o200k token counts before/after the compression that WOULD have run. It never
// mutates the request and is never booked as a saving — only surfaced as
// would_save_tokens (and, for list-price-eligible rows, a would_save_usd figure).
type estimateOutcome struct {
	before int
	after  int
}

// estimateRequest measures how many tokens compression would have removed from the
// live-zone segments of a request, WITHOUT altering the forwarded bytes and WITHOUT
// storing any CCR original. It runs only through the Estimator (engine Simulate)
// seam — a compressor that cannot estimate without storing yields no estimate, so
// the observe path can never write a recovery row. Each segment is copied before it
// reaches the engine so the original request bytes are provably untouched.
func (s *Server) estimateRequest(adapter providers.Adapter, body []byte, meta providers.RequestMetadata) *estimateOutcome {
	est, ok := s.compressor.(Estimator)
	if !ok {
		return nil
	}
	segments, _, ok := adapter.ExtractCompressible(body, meta)
	if !ok || len(segments) == 0 {
		return nil
	}
	query := extractCompressionQuery(meta.Provider, meta.Endpoint, body)
	queryEst, queryAware := s.compressor.(QueryAwareEstimator)
	var before, after int
	for _, seg := range segments {
		cp := append([]byte(nil), seg...) // never let the estimator alias forwarded bytes
		tb, ta := 0, 0
		if queryAware && query != "" {
			tb, ta = queryEst.EstimateSegmentQuery(cp, query)
		} else {
			tb, ta = est.EstimateSegment(cp)
		}
		if tb <= 0 || ta >= tb {
			continue
		}
		before += tb
		after += ta
	}
	if before <= 0 || after >= before {
		return nil
	}
	return &estimateOutcome{before: before, after: after}
}

func normalizeAgentPath(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/w/") {
		return true
	}
	slug, rest, ok := providers.SplitAgentPath(r.URL.Path)
	if !ok {
		return false
	}
	rawPath := r.URL.RawPath
	if rawPath != "" {
		rawPrefix := "/w/" + slug
		if !strings.HasPrefix(rawPath, rawPrefix) {
			return false
		}
		rawPath = strings.TrimPrefix(rawPath, rawPrefix)
		if rawPath == "" {
			return false
		}
	}
	r.URL.Path = rest
	// Preserve the encoded spelling of the stripped route. Compatibility mounts
	// reject encoded separators/dot segments before adapter selection; clearing
	// RawPath here would otherwise let a named route claim
	// `/w/slug/compat/name%2F…`.
	if rawPath != "" {
		r.URL.RawPath = rawPath
	}
	r.Header.Set("x-cave-agent", slug)
	return true
}

// compressionOutcome carries the result of an applied S4 compression from the
// compress branch through to record(): the CCR handle for the original request and
// the inferred token reduction. It is nil whenever compress mode did not run or
// did not shrink the request.
type compressionOutcome struct {
	handle  string
	handles []string
	before  int
	after   int
	ratio   float64
	// bookSavings authorizes the inferred compression saving to be claimed for this
	// request. It is true only on the server-side recovery path, where the proxy can
	// observe whether a retrieve continuation happened. Under MCP recovery the proxy
	// cannot see the agent's off-proxy retrieves, so it stays false (handle and ratio
	// are still disclosed as telemetry, but no dollar saving is claimed).
	bookSavings bool
}

// recoveryHandles returns the per-block CCR handles, nil-safe so a caller that
// merges in another path's handle does not have to branch on a strip-only request.
func (c *compressionOutcome) recoveryHandles() []string {
	if c == nil {
		return nil
	}
	return c.handles
}

// rewritableBlock is one content block the compress path may rewrite plus the
// provider-cache zone it sits in. Adapters with no cache-floor reasoning yield
// live-only blocks, which is exactly the pre-existing live-zone behavior.
type rewritableBlock struct {
	content []byte
	live    bool
	kind    string
	id      string
}

// extractRewritable asks the adapter for the blocks compress mode may rewrite.
// With a durable prefix cache AND an adapter that exposes its frozen zone, that is
// the live zone PLUS the already-cached blocks (so a replacement emitted on an
// earlier turn can be re-substituted byte-identically). Otherwise it degrades to
// the live-zone-only extraction.
func (s *Server) extractRewritable(
	adapter providers.Adapter,
	body []byte,
	meta providers.RequestMetadata,
	requireSemantics bool,
) ([]rewritableBlock, func([][]byte) ([]byte, error), bool) {
	if s.prefixCache != nil || requireSemantics {
		if stabilizer, ok := adapter.(PrefixStabilizer); ok {
			raw, reassemble, ok := stabilizer.ExtractStabilizable(body, meta)
			if !ok || len(raw) == 0 {
				return nil, nil, false
			}
			blocks := make([]rewritableBlock, len(raw))
			for i, b := range raw {
				blocks[i] = rewritableBlock{
					content: b.Content,
					live:    b.Live,
					kind:    b.Kind,
					id:      b.ID,
				}
			}
			return blocks, reassemble, true
		}
	}
	segments, reassemble, ok := adapter.ExtractCompressible(body, meta)
	if !ok || len(segments) == 0 {
		return nil, nil, false
	}
	blocks := make([]rewritableBlock, len(segments))
	for i, seg := range segments {
		blocks[i] = rewritableBlock{content: seg, live: true}
	}
	return blocks, reassemble, true
}

// compressRequest applies S4 compression to live-zone content and re-applies the
// replacements it already emitted to frozen content. Each live segment is
// independently token-gated, stored in CCR, marked inside the replacement with
// <<ccr:handle>>, and memoised in the prefix cache; each block the cache already
// knows — live or frozen — is substituted with those exact stored bytes so the
// upstream prefix is identical on every turn of the conversation. Unchanged/failed
// blocks are left byte-identical by the adapter's splice reassembler. If nothing is
// rewritten, the original body is forwarded exactly.
//
// Token accounting is once-per-message: only a block compressed for the FIRST time
// contributes to before/after. A substituted block re-books nothing — the caller
// receives its reduction on every later turn, but claiming it again would inflate
// one message's delta into a per-turn saving that was never separately earned.
func (s *Server) compressRequest(
	adapter providers.Adapter,
	body []byte,
	meta providers.RequestMetadata,
	transform *providers.TransformResult,
	requestID string,
	lockedRoutes []compiledRoute,
) *compressionOutcome {
	if s.compressor == nil {
		return nil
	}
	blocks, reassemble, ok := s.extractRewritable(adapter, body, meta, len(lockedRoutes) > 0)
	if !ok {
		return nil
	}
	routeByBlock := make([]int, len(blocks))
	for i := range routeByBlock {
		routeByBlock[i] = -1
	}
	if len(lockedRoutes) > 0 {
		if _, ok := s.compressor.(TypedCompressor); !ok {
			return nil
		}
		for blockIndex, block := range blocks {
			for routeIndex, route := range lockedRoutes {
				if route.SegmentKind != block.kind ||
					(route.SegmentID != "" && route.SegmentID != block.id) {
					continue
				}
				if routeByBlock[blockIndex] >= 0 {
					return nil // ambiguous semantic ownership: original bytes win.
				}
				routeByBlock[blockIndex] = routeIndex
			}
		}
	}
	replacements := make([][]byte, len(blocks))
	var before, after int
	var handles []string
	query := extractCompressionQuery(meta.Provider, meta.Endpoint, body)
	queryComp, queryAware := s.compressor.(QueryAwareCompressor)
	activeRoutes := make([]bool, len(lockedRoutes))
	for i, block := range blocks {
		if len(lockedRoutes) > 0 && routeByBlock[i] < 0 {
			continue // unmatched frozen history is never rewritten under this lock.
		}
		// The strip's version is part of the scope: it moves the tool catalog at the
		// head of the same provider prefix these replacements live under, so changing
		// it must retire the old entries rather than mix two prefix generations. The
		// suffix is empty while the lever is off, leaving every existing key untouched.
		cacheScope := "unlocked" + s.toolSchemaCacheScope()
		if len(lockedRoutes) > 0 {
			route := lockedRoutes[routeByBlock[i]]
			cacheScope = "locked:" + route.SegmentKind + ":" + route.SegmentID + ":" + route.TransformID + s.toolSchemaCacheScope()
		}
		if s.prefixCache != nil {
			if stored, handle, hit := s.prefixCache.LookupReplacement(cacheScope, block.content); hit {
				replacements[i] = stored
				handles = append(handles, handle)
				if len(lockedRoutes) > 0 {
					activeRoutes[routeByBlock[i]] = true
				}
				continue
			}
		}
		// A frozen block the cache does not know was never compressed by us (or its
		// entry was evicted): forward the client's original bytes. That is the
		// re-sync path — it costs one prefix rebuild and is stable from then on.
		if !block.live {
			continue
		}
		out, tb, ta := []byte(nil), 0, 0
		if len(lockedRoutes) > 0 {
			routeIndex := routeByBlock[i]
			if routeIndex < 0 {
				continue
			}
			route := lockedRoutes[routeIndex]
			typed := s.compressor.(TypedCompressor)
			out, tb, ta = typed.CompressSegmentType(block.content, route.ContentType)
			if out != nil && tb > 0 && ta < tb {
				activeRoutes[routeIndex] = true
			}
		} else if queryAware && query != "" {
			out, tb, ta = queryComp.CompressSegmentQuery(block.content, query)
		} else {
			out, tb, ta = s.compressor.CompressSegment(block.content)
		}
		if out == nil || tb <= 0 || ta >= tb {
			continue
		}
		handle, err := s.compressor.StoreOriginal(block.content)
		if err != nil || handle == "" {
			if s.logger != nil {
				if err != nil {
					s.logger.Warn("compress recovery store failed for block; keeping block original", "error", redact.Error(err), "request_id", requestID)
				} else {
					s.logger.Warn("compress recovery store returned empty handle; keeping block original", "request_id", requestID)
				}
			}
			continue
		}
		replacement := appendCCRMarker(out, handle)
		if s.prefixCache != nil {
			// A rewrite we cannot re-issue next turn must not go out at all: it would
			// diverge the prefix on the very next request. Fail open to the original.
			stored, err := s.prefixCache.RememberReplacement(cacheScope, block.content, replacement, handle)
			if err != nil || len(stored) == 0 {
				if s.logger != nil {
					s.logger.Warn("prefix replacement store failed for block; keeping block original", "error", redact.Error(err), "request_id", requestID)
				}
				continue
			}
			replacement = stored
		}
		replacements[i] = replacement
		before += tb
		after += ta
		handles = append(handles, handle)
	}
	if len(handles) == 0 {
		return nil // nothing rewritten — pass through, claim nothing.
	}
	newBody, err := reassemble(replacements)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("compress reassembly failed; forwarding original bytes unchanged", "error", redact.Error(err), "request_id", requestID)
		}
		return nil
	}
	if len(newBody) == 0 {
		return nil
	}
	if !json.Valid(newBody) {
		if s.logger != nil {
			s.logger.Warn("compress splice produced invalid JSON; forwarding original bytes unchanged", "request_id", requestID)
		}
		return nil
	}
	if bytes.Equal(newBody, body) {
		return nil
	}
	transform.Body = newBody
	if len(lockedRoutes) > 0 {
		appliedTransformIDs := make([]string, 0, len(lockedRoutes))
		for routeIndex, route := range lockedRoutes {
			if activeRoutes[routeIndex] {
				appliedTransformIDs = append(appliedTransformIDs, route.TransformID)
			}
		}
		transform.OptimizerIDs = append(transform.OptimizerIDs, appliedTransformIDs...)
	} else {
		transform.OptimizerIDs = append(transform.OptimizerIDs, compressionOptimizerID)
	}
	handleList := joinRecoveryHandles(handles)
	// A substitution-only turn rewrote bytes but earned no NEW reduction, so its
	// token fields stay at the honest zero rather than re-claiming an earlier turn's
	// delta. The handles are still disclosed — recovery must work on every turn.
	ratio := 0.0
	if before > after {
		ratio = float64(before-after) / float64(before)
	}
	return &compressionOutcome{handle: handleList, handles: handles, before: before, after: after, ratio: ratio}
}

// recoveryHandleListMax bounds the comma-joined handle list one telemetry row
// carries. The column is a debug/audit breadcrumb only — recovery itself runs off
// the `<<ccr:…>>` markers in the request body and the CCR store, never off this
// row — but every turn re-substitutes every earlier block, so an unbounded join
// puts one handle per conversation turn into EVERY row (a 40-turn session = 40
// handles a row). Duplicates collapse and the NEWEST handles are kept, because the
// block this turn actually compressed is the last one; a leading `+N` names how
// many older handles were elided, so the row is never a silent truncation.
const recoveryHandleListMax = 8

func joinRecoveryHandles(handles []string) string {
	seen := make(map[string]struct{}, len(handles))
	uniq := make([]string, 0, len(handles))
	for _, h := range handles {
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		uniq = append(uniq, h)
	}
	if len(uniq) <= recoveryHandleListMax {
		return strings.Join(uniq, ",")
	}
	elided := len(uniq) - recoveryHandleListMax
	return "+" + strconv.Itoa(elided) + "," + strings.Join(uniq[len(uniq)-recoveryHandleListMax:], ",")
}

func appendCCRMarker(out []byte, handle string) []byte {
	marker := []byte("<<ccr:" + handle + ">>")
	withMarker := make([]byte, 0, len(out)+1+len(marker))
	withMarker = append(withMarker, out...)
	if len(out) == 0 || out[len(out)-1] != '\n' {
		withMarker = append(withMarker, '\n')
	}
	withMarker = append(withMarker, marker...)
	return withMarker
}

func (s *Server) matchAdapter(r *http.Request) providers.Adapter {
	if r == nil || r.URL == nil || openaicompat.ValidateRequestPath(r.URL) != nil {
		return nil
	}
	for _, adapter := range s.adapters {
		if adapter.MatchRoute(r.Method, r.URL.Path) {
			return adapter
		}
	}
	return nil
}

// record prices the request from the catalog and writes one truthful row to the
// sink. Standalone savings are always labeled "inferred".
func (s *Server) record(start time.Time, ttfb int64, requestID, traceID string, rc RequestContext, meta providers.RequestMetadata, authMode AuthMode, status int, responseBytes int64, requestBytes int, rawHash, transformedHash [32]byte, errorCode string, optimizers []string, usage providers.UsageObservation, comp *compressionOutcome, toolSchemaHandle string, retrieved bool, estimate *estimateOutcome, evidence requestEvidence, providerCachePrefixSHA256, providerCacheComponentSHA256 string, cacheBoundaryKnown, cacheBust, compressionEligible bool) {
	price := standalonePriceForUsage(meta, usage)
	if !providers.ListPriceEligible(meta.Provider, string(authMode)) || !usage.Complete() {
		price = cost.Price{}
	}
	inputCost, outputCost, cachedCost := costBreakdown(meta.Provider, price, usage)
	total := cost.RoundUSD(inputCost + outputCost + cachedCost)
	savings := 0.0
	if authMode == AuthModePAYG && status < 400 && usage.Complete() {
		savings = cacheSavingsUSD(price, usage, optimizers)
	}
	if len(usage.CallObservations) > 0 {
		var multiInput, multiOutput, multiCached, multiSavings float64
		complete := providers.ListPriceEligible(meta.Provider, string(authMode))
		for _, callUsage := range usage.CallObservations {
			callPrice := standalonePriceForUsage(meta, callUsage)
			if !callUsage.Complete() || callPrice == (cost.Price{}) {
				complete = false
				continue
			}
			oneInput, oneOutput, oneCached := costBreakdown(meta.Provider, callPrice, callUsage)
			multiInput += oneInput
			multiOutput += oneOutput
			multiCached += oneCached
			if authMode == AuthModePAYG && status < 400 {
				multiSavings += cacheSavingsUSD(callPrice, callUsage, optimizers)
			}
		}
		if complete {
			inputCost = cost.RoundUSD(multiInput)
			outputCost = cost.RoundUSD(multiOutput)
			cachedCost = cost.RoundUSD(multiCached)
			total = cost.RoundUSD(inputCost + outputCost + cachedCost)
			savings = cost.RoundUSD(multiSavings)
		} else {
			inputCost, outputCost, cachedCost, total, savings = 0, 0, 0, 0, 0
		}
		// No single request tier describes all calls. Keep local compression's
		// dollar counterfactual at honest zero rather than misapply a tier.
		price = cost.Price{}
	}
	// Compression savings are attributed only on a successful upstream response —
	// the compressed bytes must have actually been processed for the saving to be real.
	var compRatio, compHandle = 0.0, ""
	var compBefore, compAfter int
	if comp != nil {
		compRatio, compBefore, compAfter, compHandle = comp.ratio, comp.before, comp.after, comp.handle
		if authMode == AuthModePAYG && comp.bookSavings && status < 400 && !retrieved {
			savings = cost.RoundUSD(savings + compressionSavingsUSD(price, comp.before, comp.after, optimizers, usage))
		}
	}
	// The tool-schema strip has its own CCR original. A strip-only request has no
	// compressionOutcome at all, so without this the row would name an optimizer
	// whose rewritten bytes it could not point anyone back from. It contributes a
	// handle and nothing else: the strip books no tokens and no dollars.
	if toolSchemaHandle != "" {
		handles := append([]string{}, comp.recoveryHandles()...)
		compHandle = joinRecoveryHandles(append(handles, toolSchemaHandle))
	}
	// Observe-only would-have-saved: the estimate is a pure measurement, never a
	// booked saving. would_save_tokens is the engine-counted reduction; would_save_usd
	// values it at the model's input rate ONLY when the row is list-price eligible
	// (price is empty otherwise), so an unpriced tier never fabricates a dollar figure.
	var wouldSaveTokens int
	var wouldSaveUSD *float64
	if estimate != nil && estimate.before > estimate.after {
		wouldSaveTokens = estimate.before - estimate.after
		if price != (cost.Price{}) {
			v := cost.RoundUSD(cost.EstimateUSD(
				cost.Price{InputPerMillion: price.InputPerMillion},
				cost.Usage{InputTokens: wouldSaveTokens},
			))
			wouldSaveUSD = &v
		}
	}
	cacheStatus := usage.CacheStatus
	if cacheStatus == "" {
		cacheStatus = "unknown"
	}
	s.sink.Record(RequestRecord{
		Timestamp:                    time.Now().UTC().Format("2006-01-02 15:04:05.000"),
		RequestID:                    requestID,
		TraceID:                      traceID,
		Label:                        labelOrDefault(rc.Label, "local"),
		SessionID:                    evidence.SessionID,
		SessionCorrelationBasis:      evidence.SessionCorrelationBasis,
		AgentBuildSHA256:             evidence.AgentBuildSHA256,
		EfficiencyPlanSHA256:         evidence.EfficiencyPlanSHA256,
		ContextBill:                  evidence.ContextBill,
		TransformTrace:               evidence.TransformTrace,
		TransformLocation:            evidence.TransformLocation,
		CacheEpoch:                   evidence.CacheEpoch,
		CachePrefixSHA256:            evidence.CachePrefixSHA256,
		ProviderCachePrefixSHA256:    providerCachePrefixSHA256,
		ProviderCacheComponentSHA256: providerCacheComponentSHA256,
		CacheBoundaryKnown:           cacheBoundaryKnown,
		CacheBust:                    cacheBust,
		CompressionEligible:          compressionEligible,
		AgentSlug:                    labelOrDefault(rc.AgentSlug, "unlabeled-agent"),
		Provider:                     meta.Provider,
		Model:                        meta.Model,
		RouteFrom:                    meta.Model,
		RouteTo:                      meta.Model,
		Endpoint:                     meta.Endpoint,
		Stream:                       meta.Stream,
		StatusCode:                   status,
		ErrorCode:                    errorCode,
		LatencyMS:                    time.Since(start).Milliseconds(),
		TTFBMS:                       ttfb,
		RequestBytes:                 requestBytes,
		ResponseBytes:                responseBytes,
		InputTokens:                  usage.InputTokens,
		OutputTokens:                 usage.OutputTokens,
		CachedInputTokens:            usage.CachedInputTokens,
		CacheCreationInputTokens:     usage.CacheCreationInputTokens,
		ReasoningTokens:              usage.ReasoningTokens,
		TotalCostUSD:                 total,
		SavingsUSD:                   savings,
		Basis:                        "inferred",
		TokenUsageBasis:              standaloneUsageBasis(usage),
		AuthMode:                     string(authMode),
		RuntimeMode:                  rc.RuntimeMode,
		OptimizationIDs:              optimizers,
		CacheStatus:                  cacheStatus,
		RawRequestSHA256:             hex.EncodeToString(rawHash[:]),
		TransformedRequestSHA256:     hex.EncodeToString(transformedHash[:]),
		RequestHashComplete:          true,
		CompressionRatio:             compRatio,
		CompressionTokensBefore:      compBefore,
		CompressionTokensAfter:       compAfter,
		CompressionTokenCountBasis:   standaloneCompressionBasis(comp),
		RecoveryHandle:               compHandle,
		WouldSaveTokens:              wouldSaveTokens,
		WouldSaveUSD:                 wouldSaveUSD,
	})
}

// costBreakdown prices normalized provider usage. Cache and reasoning fields are
// subsets of the inclusive input/output totals and are never added twice.
func costBreakdown(provider string, price cost.Price, usage providers.UsageObservation) (inputCost, outputCost, cachedCost float64) {
	if usage.Malformed || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CachedInputTokens < 0 || usage.CacheCreationInputTokens < 0 || usage.CacheCreation1hTokens < 0 || usage.ReasoningTokens < 0 {
		return 0, 0, 0
	}
	price = cost.ForInputTokens(price, usage.InputTokens)
	billableInput := usage.InputTokens - usage.CachedInputTokens - usage.CacheCreationInputTokens
	if billableInput < 0 || usage.ReasoningTokens > usage.OutputTokens || usage.CacheCreation1hTokens > usage.CacheCreationInputTokens {
		return 0, 0, 0
	}
	cacheWrite5m := usage.CacheCreationInputTokens - usage.CacheCreation1hTokens
	inputCost = cost.EstimateUSD(cost.Price{InputPerMillion: price.InputPerMillion}, cost.Usage{InputTokens: billableInput})
	visibleOutput := usage.OutputTokens - usage.ReasoningTokens
	reasoningRate := price.ReasoningPerMillion
	if reasoningRate == 0 {
		reasoningRate = price.OutputPerMillion
	}
	outputCost = cost.EstimateUSD(
		cost.Price{OutputPerMillion: price.OutputPerMillion, ReasoningPerMillion: reasoningRate},
		cost.Usage{OutputTokens: visibleOutput, ReasoningTokens: usage.ReasoningTokens},
	)
	cachedCost = cost.EstimateUSD(
		cost.Price{CacheReadPerMillion: price.CacheReadPerMillion, CacheWritePerMillion: price.CacheWritePerMillion, CacheWrite1hPerMillion: price.CacheWrite1hPerMillion},
		cost.Usage{CachedInputTokens: usage.CachedInputTokens, CacheCreationTokens: cacheWrite5m, CacheCreation1hTokens: usage.CacheCreation1hTokens},
	)
	return inputCost, outputCost, cachedCost
}

func standalonePriceForUsage(meta providers.RequestMetadata, usage providers.UsageObservation) cost.Price {
	if meta.PricingUnsupportedReason != "" || usage.PricingUnsupportedReason != "" {
		return cost.Price{}
	}
	if meta.Provider == "gemini" && strings.ToLower(strings.TrimSpace(meta.BillingTier)) != "paid" {
		return cost.Price{}
	}
	tier := strings.ToLower(strings.TrimSpace(usage.ServiceTier))
	if tier == "" {
		tier = strings.ToLower(strings.TrimSpace(meta.ServiceTier))
	}
	switch tier {
	case "", "default", "standard", "standard_only", "unspecified":
	default:
		return cost.Price{}
	}
	var price cost.Price
	switch meta.Provider {
	case "bedrock":
		price, _ = catalog.PriceForRegion(meta.Provider, meta.Model, meta.Region)
	case "vertex":
		price, _ = catalog.PriceForRegionOrAgnostic(meta.Provider, meta.Model, meta.Region)
	default:
		price, _ = catalog.Price(meta.Provider, meta.Model)
	}
	if meta.Provider == "openai" && (meta.Region == "us" || meta.Region == "eu") {
		multiplier, ok := catalog.PricingMultiplier("openai", meta.Model, "global", "regional_processing_multiplier")
		if !ok {
			return cost.Price{}
		}
		price = scaleStandaloneTokenRates(price, multiplier)
	}
	geo := strings.ToLower(strings.TrimSpace(usage.InferenceGeo))
	if geo == "" {
		geo = strings.ToLower(strings.TrimSpace(meta.InferenceGeo))
	}
	if geo != "" && geo != "global" {
		if meta.Provider != "anthropic" || geo != "us" {
			return cost.Price{}
		}
		multiplier, ok := catalog.PricingMultiplier("anthropic", meta.Model, "global", "inference_geo_us_multiplier")
		if !ok {
			return cost.Price{}
		}
		price = scaleStandaloneTokenRates(price, multiplier)
	}
	return price
}

func scaleStandaloneTokenRates(price cost.Price, multiplier float64) cost.Price {
	price.InputPerMillion *= multiplier
	price.OutputPerMillion *= multiplier
	price.CacheReadPerMillion *= multiplier
	price.CacheWritePerMillion *= multiplier
	price.CacheWrite1hPerMillion *= multiplier
	price.ReasoningPerMillion *= multiplier
	return price
}

func standaloneUsageBasis(usage providers.UsageObservation) string {
	if usage.Malformed {
		return "provider_malformed"
	}
	if usage.InputTokensReported && usage.OutputTokensReported {
		return "provider_complete"
	}
	if usage.InputTokensReported || usage.OutputTokensReported {
		return "provider_partial"
	}
	return "unavailable"
}

func standaloneCompressionBasis(comp *compressionOutcome) string {
	if comp == nil {
		return ""
	}
	return "estimated_engine_o200k"
}

// cacheSavingsUSD is the baseline-vs-optimized delta for a request whose caching
// was caused by a Caveman cache optimizer. It is gated on a CACHE optimizer
// having been applied so cache hits the caller produced themselves are not
// attributed to us. In standalone the result is recorded as `inferred` — never
// `verified` — because there is no cloud eval gate behind it.
func cacheSavingsUSD(price cost.Price, usage providers.UsageObservation, optimizers []string) float64 {
	if !hasCacheOptimizer(optimizers) {
		return 0
	}
	if usage.CachedInputTokens == 0 && usage.CacheCreationInputTokens == 0 {
		return 0
	}
	price = cost.ForInputTokens(price, usage.InputTokens)
	if usage.CachedInputTokens < 0 || usage.CacheCreationInputTokens < 0 || usage.CacheCreation1hTokens < 0 || usage.CacheCreation1hTokens > usage.CacheCreationInputTokens || usage.CachedInputTokens+usage.CacheCreationInputTokens > usage.InputTokens {
		return 0
	}
	baselineCached := cost.EstimateUSD(
		cost.Price{InputPerMillion: price.InputPerMillion},
		cost.Usage{InputTokens: usage.CachedInputTokens + usage.CacheCreationInputTokens},
	)
	actualCached := cost.EstimateUSD(
		cost.Price{CacheReadPerMillion: price.CacheReadPerMillion, CacheWritePerMillion: price.CacheWritePerMillion, CacheWrite1hPerMillion: price.CacheWrite1hPerMillion},
		cost.Usage{CachedInputTokens: usage.CachedInputTokens, CacheCreationTokens: usage.CacheCreationInputTokens - usage.CacheCreation1hTokens, CacheCreation1hTokens: usage.CacheCreation1hTokens},
	)
	return cost.RoundUSD(baselineCached - actualCached)
}

// cacheOptimizerIDs is the set of optimizers that actually cause provider-native
// caching. Only these may attribute cache savings; behavioral/affinity hints must
// not. OpenAI prompt_cache_key is deliberately absent: OpenAI caching is already
// automatic and the affinity hint alone cannot prove an incremental cache hit.
// The historical gemini-explicit-cache scaffold is permanently absent: it never
// created a provider cache and is retired from policy and practice surfaces.
var cacheOptimizerIDs = map[string]bool{
	"anthropic-cache-breakpoints": true,
}

func hasCacheOptimizer(optimizers []string) bool {
	for _, oid := range optimizers {
		if cacheOptimizerIDs[oid] {
			return true
		}
	}
	return false
}

// compressionOptimizerID is the policy/telemetry id attributed when S4 content
// compression shrank a request. It is the gate for compression savings — a request
// the proxy did not compress can never carry it.
const compressionOptimizerID = "caveman-compression"

// compressionOptimizerIDs is the set of optimizer ids that may attribute
// compression savings. Mirrors cacheOptimizerIDs so the two savings paths share one
// discipline: a saving is only ever attributed to an optimizer we actually ran.
var compressionOptimizerIDs = map[string]bool{compressionOptimizerID: true}

func hasCompressionOptimizer(optimizers []string) bool {
	for _, oid := range optimizers {
		if compressionOptimizerIDs[oid] ||
			(strings.HasPrefix(oid, "caveman.engine.") && strings.HasSuffix(oid, ".v1")) {
			return true
		}
	}
	return false
}

// compressionSavingsUSD is the inferred dollar value of the input tokens that S4
// content compression removed before the request reached the provider. It is gated
// on the compression optimizer id so a request we did not compress can never
// attribute a saving, and it returns 0 unless the result was actually smaller. In
// standalone the figure is recorded as `inferred` — never `verified`; this mirrors
// the cloud gateway's verifiedCacheSavings gating discipline (the gate is the
// point, not the label).
//
// The removed tokens are valued at the model's FULL input rate only when the
// request was not predominantly served from cache. On cache-heavy traffic, valuing
// them at the full input rate overstates the saving ~10x — the tokens compression
// displaced would have been billed at the far cheaper cache-read rate. So, mirroring
// cacheSavingsUSD's honest counterfactual and the honesty-culture "under-claim,
// never blend" rule, when cache reads dominated the billed input the removed tokens
// are valued at the cache-read rate instead (issue #133).
func compressionSavingsUSD(price cost.Price, before, after int, optimizers []string, usage providers.UsageObservation) float64 {
	if !hasCompressionOptimizer(optimizers) {
		return 0
	}
	if before <= after {
		return 0
	}
	rate := price.InputPerMillion
	if usage.CachedInputTokens >= 0 && usage.CacheCreationInputTokens >= 0 {
		billableFresh := usage.InputTokens - usage.CachedInputTokens - usage.CacheCreationInputTokens
		if usage.CachedInputTokens > 0 && usage.CachedInputTokens >= billableFresh && price.CacheReadPerMillion > 0 {
			rate = price.CacheReadPerMillion
		}
	}
	return cost.RoundUSD(cost.EstimateUSD(
		cost.Price{InputPerMillion: rate},
		cost.Usage{InputTokens: before - after},
	))
}

func labelOrDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

type countingWriter struct {
	w           http.ResponseWriter
	n           int64
	firstByteAt time.Time
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.firstByteAt.IsZero() && len(p) > 0 {
		c.firstByteAt = time.Now()
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func copyFlush(dst *countingWriter, src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			if f, ok := dst.w.(http.Flusher); ok {
				f.Flush()
			}
			written += int64(nw)
			if ew != nil {
				return written, ew
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if er != nil {
			if er != io.EOF {
				return written, er
			}
			break
		}
	}
	return written, nil
}

func traceIDFrom(r *http.Request) string {
	if v := r.Header.Get("x-cave-trace-id"); len(v) == 32 {
		return v
	}
	if tp := r.Header.Get("traceparent"); len(tp) >= 55 {
		return tp[3:35]
	}
	return id.NewTraceID()
}
