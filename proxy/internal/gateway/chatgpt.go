package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/shared/platform/httpx"
	"github.com/JuliusBrussee/caveman/shared/platform/id"
)

// DefaultChatGPTUpstream is the ChatGPT-subscription Codex backend. A wrapped
// Codex CLI on a ChatGPT login reaches it through this proxy via a custom
// model_provider with requires_openai_auth=true and base_url .../chatgpt.
const DefaultChatGPTUpstream = "https://chatgpt.com/backend-api/codex"

// chatGPTCaptureLimit caps request transformation and opportunistic response
// parsing. Bigger bodies stream through byte-exact and record no invented counts.
const chatGPTCaptureLimit = 4 << 20

// chatgpt is the ChatGPT-subscription Codex route. It preserves the agent's OAuth
// headers and streams responses unchanged. An entitled local compress run with
// agent-owned MCP recovery may apply the same schema-aware, deterministic live-zone
// compression as the OpenAI Responses adapter; every other case is pass-through.
// Invariants:
//   - no credential resolution, no env fallback — the agent's own OAuth
//     Authorization + ChatGPT-Account-ID headers ride through untouched, and
//     they are never logged, cached, or substituted. A missing credential is
//     the upstream's 401, not ours.
//   - only /responses request content may be transformed, only through the
//     account+MCP+prefix-stability gate. Any parse/store/shrink failure forwards
//     original bytes; a transformed 4xx retries once with original bytes.
//   - response bodies remain byte-identical and SSE streams stay unbuffered.
//   - metering is opportunistic and honest: parseable usage records token
//     counts with TotalCostUSD 0 — subscription traffic has no per-token
//     price, and pricing it at API rates would be a fake number.
func (s *Server) chatgpt(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := id.NewUUIDv7()
	traceID := traceIDFrom(r)
	w.Header().Set("x-cave-request-id", requestID)
	w.Header().Set("x-cave-trace-id", traceID)

	rc, err := s.auth.Authenticate(r.Context(), r)
	if err != nil {
		httpx.Error(w, r, http.StatusUnauthorized, "cave_unauthorized", "Request rejected by the proxy authenticator.")
		return
	}
	rc.AgentSlug = labelOrDefault(r.Header.Get("x-cave-agent"), "unlabeled-agent")
	evidence := requestEvidenceFromHeaders(r.Header)
	lockedRoutes, compiledPlanAllowed := compiledPlanRoutes(r.Header)

	suffix := strings.TrimPrefix(r.URL.Path, "/chatgpt")
	if suffix == "" {
		suffix = "/"
	}
	upstreamURL := s.chatGPTUpstream + suffix
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	// Compression needs a complete request. Keep the existing bounded behavior:
	// over-limit bodies stream through unchanged rather than being rejected or held
	// unbounded in memory.
	reqCapture := &cappedBuffer{limit: chatGPTCaptureLimit}
	reqHash := sha256.New()
	var reqBody io.Reader
	var originalBody []byte
	var requestBodyFullyRead bool
	var requestBodyTracker *eofTrackingReader
	transform := providers.TransformResult{OptimizerIDs: []string{}}
	var comp *compressionOutcome
	adapter := openai.New(s.chatGPTUpstream)
	compressEligible := r.Method == http.MethodPost && rc.RuntimeMode == "compress" && suffix == "/responses" &&
		s.compressor != nil && s.liveZoneCompressionAllowed(adapter) && compiledPlanAllowed
	if compressEligible {
		captured, readErr := io.ReadAll(io.LimitReader(r.Body, chatGPTCaptureLimit+1))
		if readErr == nil && len(captured) <= chatGPTCaptureLimit {
			requestBodyFullyRead = true
			originalBody = captured
			_, _ = reqHash.Write(originalBody)
			_, _ = reqCapture.Write(originalBody)
			transform.Body = originalBody
			headersForInspect := r.Header.Clone()
			headersForInspect.Set("x-cave-route-path", suffix)
			meta, inspectErr := adapter.InspectRequest(r.Context(), bytes.NewReader(originalBody), headersForInspect)
			if inspectErr == nil {
				meta.Endpoint = suffix
				meta.SessionID = evidence.SessionID
				if s.cacheEpochAllows(r, adapter, meta, originalBody, evidence.SessionID) {
					comp = s.compressRequest(adapter, originalBody, meta, &transform, requestID, lockedRoutes)
				}
			}
			reqBody = bytes.NewReader(transform.Body)
		} else {
			// Reconstruct the consumed prefix and continue the old streaming path.
			source := io.MultiReader(bytes.NewReader(captured), r.Body)
			requestBodyTracker = &eofTrackingReader{reader: source}
			reqBody = io.TeeReader(io.TeeReader(requestBodyTracker, reqHash), reqCapture)
			transform.Body = nil
		}
	} else {
		requestBodyTracker = &eofTrackingReader{reader: r.Body}
		reqBody = io.TeeReader(io.TeeReader(requestBodyTracker, reqHash), reqCapture)
	}
	if r.ContentLength == 0 && (r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodDelete) {
		// A bodyless method with a non-nil reader would go out chunked; keep
		// the wire shape identical to what the agent sent.
		reqBody = nil
	}

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, reqBody)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "cave_provider_request_invalid", "ChatGPT route could not build the upstream request.")
		return
	}
	if transform.Body != nil {
		upReq.ContentLength = int64(len(transform.Body))
	} else {
		upReq.ContentLength = r.ContentLength
	}
	// Preserve OAuth/account/application headers, but never forward proxy-private
	// metadata or hop-by-hop fields to the subscription backend.
	upReq.Header = chatGPTRequestHeaders(r.Header)
	if comp != nil {
		upReq.Header.Del("Content-Length")
	}

	// Capture both sides of the transform before the send (see capture.go), off
	// unless CAVE_CAPTURE_DIR is set. Only the fully-read path can do it here; the
	// streaming path has no complete bytes or hash until the body has been
	// forwarded, so it captures after the response instead.
	if originalBody != nil {
		s.capture.record(captureMeta{
			RequestID:   requestID,
			Provider:    "chatgpt-subscription",
			Endpoint:    suffix,
			RuntimeMode: rc.RuntimeMode,
			Optimizers:  strings.Join(transform.OptimizerIDs, ","),
		}, wholeBody(originalBody), wholeBody(transform.Body))
	}

	resp, err := s.httpClient.Do(upReq)
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unreachable", "ChatGPT upstream is unreachable.")
		requestHashComplete := chatGPTRequestHashComplete(requestBodyFullyRead, r.ContentLength, reqCapture, requestBodyTracker)
		s.recordChatGPT(rc, r, requestID, traceID, suffix, start, 0, "cave_upstream_unreachable", reqCapture, reqHash.Sum(nil), transformedChatGPTHash(reqHash.Sum(nil), transform.Body), requestHashComplete, nil, 0, false, transform.OptimizerIDs, comp)
		return
	}
	// OAuth backends can reject byte-modified requests for undocumented reasons.
	// Retry once with exact original bytes, then disclose/record no optimization.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && comp != nil && originalBody != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		retryReq, retryErr := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(originalBody))
		if retryErr == nil {
			retryReq.Header = chatGPTRequestHeaders(r.Header)
			retryReq.Header.Del("Content-Length")
			retryResp, doErr := s.httpClient.Do(retryReq)
			if doErr == nil {
				resp = retryResp
				transform = providers.TransformResult{Body: originalBody, OptimizerIDs: []string{}}
				comp = nil
				// The original bytes served the request; the capture written before
				// the first attempt describes bytes the upstream rejected. Both
				// attempts stay on disk, and this one says which served.
				s.capture.record(captureMeta{
					RequestID:     requestID,
					Provider:      "chatgpt-subscription",
					Endpoint:      suffix,
					RuntimeMode:   rc.RuntimeMode,
					RetryOriginal: true,
				}, wholeBody(originalBody), wholeBody(originalBody))
			} else {
				httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unreachable", "ChatGPT upstream is unreachable.")
				s.recordChatGPT(rc, r, requestID, traceID, suffix, start, 0, "cave_upstream_unreachable", reqCapture, reqHash.Sum(nil), reqHash.Sum(nil), true, nil, 0, false, nil, nil)
				return
			}
		}
	}
	defer resp.Body.Close()

	copySafeResponseHeaders(w.Header(), resp.Header)
	if comp != nil {
		w.Header().Set("x-cave-mode", rc.RuntimeMode)
		w.Header().Set("x-cave-optimization", strings.Join(transform.OptimizerIDs, ","))
		w.Header().Set("x-caveman-compression-ratio", strconv.FormatFloat(comp.ratio, 'f', 4, 64))
		w.Header().Set("x-caveman-recovery-handle", comp.handle)
		w.Header().Set("x-caveman-tokens-before", strconv.Itoa(comp.before))
		w.Header().Set("x-caveman-tokens-after", strconv.Itoa(comp.after))
		w.Header().Set("x-caveman-token-count-basis", "estimated_engine_o200k")
	}
	w.WriteHeader(resp.StatusCode)

	respCapture := &cappedBuffer{limit: chatGPTCaptureLimit}
	respBytes, stream := s.streamThrough(w, io.TeeReader(resp.Body, respCapture))

	// The streaming path forwarded the request without ever holding it whole, so it
	// captures only what it can state truthfully: the whole body's length and hash,
	// plus the bytes themselves when the bounded buffer happened to hold all of
	// them. This path never transforms, so both sides are the same body.
	if s.capture != nil && originalBody == nil {
		sent := streamedBody(reqCapture.total, hex.EncodeToString(reqHash.Sum(nil)))
		if !reqCapture.truncated {
			sent = wholeBody(append([]byte(nil), reqCapture.buf.Bytes()...))
		}
		s.capture.record(captureMeta{
			RequestID:   requestID,
			Provider:    "chatgpt-subscription",
			Endpoint:    suffix,
			RuntimeMode: rc.RuntimeMode,
		}, sent, sent)
	}

	requestHashComplete := chatGPTRequestHashComplete(requestBodyFullyRead, r.ContentLength, reqCapture, requestBodyTracker)
	s.recordChatGPT(rc, r, requestID, traceID, suffix, start, resp.StatusCode, "", reqCapture, reqHash.Sum(nil), transformedChatGPTHash(reqHash.Sum(nil), transform.Body), requestHashComplete, respCapture, respBytes, stream, transform.OptimizerIDs, comp)

	// Path, status, and timing only — request headers carry the operator's
	// OAuth credential and are never logged on this route.
	if s.logger != nil {
		s.logger.Info("chatgpt_proxy",
			"path", suffix, "status", resp.StatusCode,
			"latency_ms", time.Since(start).Milliseconds(), "stream", stream, "compressed", comp != nil)
	}
}

func transformedChatGPTHash(raw []byte, transformed []byte) []byte {
	if transformed == nil {
		return raw
	}
	sum := sha256.Sum256(transformed)
	return sum[:]
}

// streamThrough copies upstream bytes to the client, flushing per chunk so SSE
// arrives unbuffered. It returns the byte count and whether flushing happened
// mid-stream (a streaming response).
func (s *Server) streamThrough(w http.ResponseWriter, body io.Reader) (int64, bool) {
	flusher, canFlush := w.(http.Flusher)
	var total int64
	chunks := 0
	buf := make([]byte, 32*1024)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return total, chunks > 1
			}
			total += int64(n)
			chunks++
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			return total, chunks > 1
		}
	}
}

func (s *Server) recordChatGPT(rc RequestContext, r *http.Request, requestID, traceID, endpoint string, start time.Time, status int, errCode string, reqCapture *cappedBuffer, reqHash, transformedHash []byte, requestHashComplete bool, respCapture *cappedBuffer, respBytes int64, stream bool, optimizers []string, comp *compressionOutcome) {
	if s.sink == nil {
		return
	}
	var usage providers.UsageObservation
	// Opportunistic and honest: a truncated capture is never parsed — partial
	// SSE could yield partial counters, and no number beats a wrong number.
	if respCapture != nil && !respCapture.truncated {
		providers.ParseUsageBytes("openai", respCapture.buf.Bytes(), &usage)
	}
	model := "unknown"
	if reqCapture != nil && !reqCapture.truncated {
		var body struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(reqCapture.buf.Bytes(), &body) == nil && body.Model != "" {
			model = body.Model
		}
	}
	rawHash, transformedHashHex := "", ""
	if requestHashComplete {
		rawHash = hex.EncodeToString(reqHash)
		transformedHashHex = hex.EncodeToString(transformedHash)
	}
	var compRatio float64
	var compBefore, compAfter int
	var compHandle, compBasis string
	if comp != nil {
		compRatio, compBefore, compAfter, compHandle = comp.ratio, comp.before, comp.after, comp.handle
		if comp.before > comp.after {
			compBasis = "estimated_engine_o200k"
		}
	}
	s.sink.Record(RequestRecord{
		Timestamp:                start.UTC().Format(time.RFC3339Nano),
		RequestID:                requestID,
		TraceID:                  traceID,
		Label:                    labelOrDefault(rc.Label, "local"),
		AgentSlug:                rc.AgentSlug,
		Provider:                 "chatgpt-subscription",
		Model:                    model,
		RouteFrom:                r.URL.Path,
		RouteTo:                  s.chatGPTUpstream + endpoint,
		Endpoint:                 endpoint,
		Stream:                   stream,
		StatusCode:               status,
		ErrorCode:                errCode,
		LatencyMS:                time.Since(start).Milliseconds(),
		RequestBytes:             reqCapture.total,
		ResponseBytes:            respBytes,
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CachedInputTokens:        usage.CachedInputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		ReasoningTokens:          usage.ReasoningTokens,
		// Subscription traffic is unpriced: zero dollars, never an API-rate
		// guess (no-fake-savings). Token counts above are the honest meter.
		TotalCostUSD:               0,
		SavingsUSD:                 0,
		Basis:                      "inferred",
		TokenUsageBasis:            standaloneUsageBasis(usage),
		AuthMode:                   string(AuthModeSubscription),
		RuntimeMode:                rc.RuntimeMode,
		OptimizationIDs:            optimizers,
		RawRequestSHA256:           rawHash,
		TransformedRequestSHA256:   transformedHashHex,
		RequestHashComplete:        requestHashComplete,
		CompressionRatio:           compRatio,
		CompressionTokensBefore:    compBefore,
		CompressionTokensAfter:     compAfter,
		CompressionTokenCountBasis: compBasis,
		RecoveryHandle:             compHandle,
	})
}

type eofTrackingReader struct {
	reader io.Reader
	sawEOF bool
}

func (r *eofTrackingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF {
		r.sawEOF = true
	}
	return n, err
}

func chatGPTRequestHashComplete(fullyRead bool, contentLength int64, capture *cappedBuffer, tracker *eofTrackingReader) bool {
	if fullyRead {
		return true
	}
	if capture != nil && contentLength >= 0 && int64(capture.total) == contentLength {
		return true
	}
	return tracker != nil && tracker.sawEOF
}

// cappedBuffer retains up to limit bytes and records the true total; past the
// limit it flags truncation instead of growing (bounded memory, no partial
// parses downstream).
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	total     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.total += len(p)
	if c.truncated {
		return len(p), nil
	}
	room := c.limit - c.buf.Len()
	if room <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		c.buf.Write(p[:room])
		c.truncated = true
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}
