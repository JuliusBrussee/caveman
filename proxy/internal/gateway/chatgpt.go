package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
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
//   - response bodies remain byte-exact, except a complete terminal SSE event
//     gets its missing blank-line delimiter so Pi can dispatch it at EOF.
//     A stream without a terminal Responses event fails closed.
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
		s.rejectUnauthorized(w, r)
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
		// nil body: this route exists only for subscription wrap, which proves
		// recovery out of band before starting its dedicated proxy.
		s.compressor != nil && s.liveZoneCompressionAllowed(adapter, nil) && compiledPlanAllowed
	if compressEligible {
		captured, readErr := io.ReadAll(io.LimitReader(r.Body, chatGPTCaptureLimit+1))
		if readErr == nil && len(captured) <= chatGPTCaptureLimit {
			requestBodyFullyRead = true
			originalBody = captured
			_, _ = reqHash.Write(originalBody)
			_, _ = reqCapture.Write(originalBody)

			logicalBody, requestEncoding, decodable := decodeChatGPTRequestBody(originalBody, r.Header.Get("Content-Encoding"))
			if decodable {
				transform.Body = logicalBody
				headersForInspect := r.Header.Clone()
				headersForInspect.Del("Content-Encoding")
				headersForInspect.Set("x-cave-route-path", suffix)
				meta, inspectErr := adapter.InspectRequest(r.Context(), bytes.NewReader(logicalBody), headersForInspect)
				if inspectErr == nil {
					meta.Endpoint = suffix
					meta.SessionID = evidence.SessionID
					if s.cacheEpochAllows(r, adapter, meta, logicalBody, evidence.SessionID) {
						comp = s.compressRequest(adapter, logicalBody, meta, &transform, requestID, lockedRoutes)
					}
				}
				if comp != nil {
					if encoded, ok := encodeChatGPTRequestBody(transform.Body, requestEncoding); ok {
						transform.Body = encoded
					} else {
						transform = providers.TransformResult{Body: originalBody, OptimizerIDs: []string{}}
						comp = nil
					}
				} else {
					// No logical transform means no reason to perturb Pi's
					// original zstd frame.
					transform.Body = originalBody
				}
			} else {
				// Unknown or malformed encodings remain exact pass-through.
				transform.Body = originalBody
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

	// The fully-buffered path can rebuild its body per attempt, so transient
	// transport failures retry instead of surfacing a terminal 502. The
	// streaming path has a partially-consumed body and cannot replay.
	var resp *http.Response
	if transform.Body != nil {
		resp, err = s.doUpstream(r.Context(), func() (*http.Request, error) {
			req, buildErr := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(transform.Body))
			if buildErr != nil {
				return nil, buildErr
			}
			req.ContentLength = int64(len(transform.Body))
			req.Header = chatGPTRequestHeaders(r.Header)
			if comp != nil {
				req.Header.Del("Content-Length")
			}
			return req, nil
		})
	} else {
		resp, err = s.httpClient.Do(upReq)
	}
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unreachable", "ChatGPT upstream is unreachable.")
		requestHashComplete := chatGPTRequestHashComplete(requestBodyFullyRead, r.ContentLength, reqCapture, requestBodyTracker)
		s.recordChatGPT(rc, r, requestID, traceID, suffix, start, 0, "cave_upstream_unreachable", reqCapture, reqHash.Sum(nil), transformedChatGPTHash(reqHash.Sum(nil), transform.Body), requestHashComplete, nil, 0, false, transform.OptimizerIDs, comp, compressEligible, transform.Body)
		return
	}
	// OAuth backends can reject byte-modified requests for undocumented reasons.
	// Retry once with exact original bytes, then disclose/record no optimization.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && comp != nil && originalBody != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		retryResp, doErr := s.doUpstream(r.Context(), func() (*http.Request, error) {
			retryReq, retryErr := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(originalBody))
			if retryErr != nil {
				return nil, retryErr
			}
			retryReq.Header = chatGPTRequestHeaders(r.Header)
			retryReq.Header.Del("Content-Length")
			return retryReq, nil
		})
		if doErr != nil {
			httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unreachable", "ChatGPT upstream is unreachable.")
			s.recordChatGPT(rc, r, requestID, traceID, suffix, start, 0, "cave_upstream_unreachable", reqCapture, reqHash.Sum(nil), reqHash.Sum(nil), true, nil, 0, false, nil, nil, compressEligible, originalBody)
			return
		}
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
	respCapture := &cappedBuffer{limit: chatGPTCaptureLimit}
	piResponsesRoute := rc.AgentSlug == "pi" && r.Method == http.MethodPost && suffix == "/responses"
	requestBodyComplete := chatGPTRequestHashComplete(requestBodyFullyRead, r.ContentLength, reqCapture, requestBodyTracker)
	piResponsesStream := piResponsesRoute && chatGPTRequestWantsStream(reqCapture.buf.Bytes(), r.Header.Get("Content-Encoding"), reqCapture.truncated, requestBodyComplete)
	stream := streamingResponse(resp.Header) || piResponsesStream
	var responseBody io.Reader = resp.Body
	var completionTracker streamCompletionTracker
	if piResponsesRoute && stream && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest && (piResponsesStream || isChatGPTEventStream(resp.Header)) {
		completionReader := &chatGPTSSECompletionReader{source: responseBody}
		responseBody = completionReader
		completionTracker = completionReader
		// EOF normalization can append a delimiter; upstream's length no longer applies.
		w.Header().Del("Content-Length")
	}
	w.WriteHeader(resp.StatusCode)
	counter, errCode := s.streamResponse(w, r, io.TeeReader(responseBody, respCapture), stream, requestID, completionTracker)
	respBytes := counter.n

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
	s.recordChatGPT(rc, r, requestID, traceID, suffix, start, resp.StatusCode, errCode, reqCapture, reqHash.Sum(nil), transformedChatGPTHash(reqHash.Sum(nil), transform.Body), requestHashComplete, respCapture, respBytes, stream, transform.OptimizerIDs, comp, compressEligible, transform.Body)

	// Path, status, and timing only — request headers carry the operator's
	// OAuth credential and are never logged on this route.
	if s.logger != nil {
		s.logger.Info("chatgpt_proxy",
			"path", suffix, "status", resp.StatusCode,
			"latency_ms", time.Since(start).Milliseconds(), "stream", stream, "compressed", comp != nil, "error_code", errCode)
	}
	if errCode != "" {
		// Same contract as the provider proxy: the row is recorded, then framing
		// is aborted so a partial body is never a clean EOF.
		panic(http.ErrAbortHandler)
	}
}

func transformedChatGPTHash(raw []byte, transformed []byte) []byte {
	if transformed == nil {
		return raw
	}
	sum := sha256.Sum256(transformed)
	return sum[:]
}

func (s *Server) recordChatGPT(rc RequestContext, r *http.Request, requestID, traceID, endpoint string, start time.Time, status int, errCode string, reqCapture *cappedBuffer, reqHash, transformedHash []byte, requestHashComplete bool, respCapture *cappedBuffer, respBytes int64, stream bool, optimizers []string, comp *compressionOutcome, compressionEligible bool, acceptedBody []byte) {
	if s.sink == nil {
		return
	}
	var usage providers.UsageObservation
	// Opportunistic and honest: a truncated capture is never parsed — partial
	// SSE could yield partial counters, and no number beats a wrong number.
	if respCapture != nil && !respCapture.truncated && errCode != "cave_upstream_body_read_failed" && errCode != "cave_client_canceled" {
		providers.ParseUsageBytes("openai", respCapture.buf.Bytes(), &usage)
	}
	var originalLogicalBody []byte
	if requestHashComplete && reqCapture != nil && !reqCapture.truncated {
		if decoded, _, ok := decodeChatGPTRequestBody(reqCapture.buf.Bytes(), r.Header.Get("Content-Encoding")); ok {
			originalLogicalBody = decoded
		}
	}
	model := "unknown"
	if originalLogicalBody != nil {
		var body struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(originalLogicalBody, &body) == nil && body.Model != "" {
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
	if comp != nil && status >= 200 && status < 300 && errCode == "" && !usage.ProviderError {
		compRatio, compBefore, compAfter, compHandle = comp.ratio, comp.before, comp.after, comp.handle
		if comp.before > comp.after {
			compBasis = "estimated_engine_o200k"
		}
	}
	row := RequestRecord{
		// storeTSLayout, not RFC3339Nano: the store's `ts` column is space-separated
		// and compared/ordered as text. 'T' sorts after ' ', so RFC3339 rows landed
		// on the wrong side of every `--since` bound and mis-sorted under ORDER BY ts.
		Timestamp: start.UTC().Format("2006-01-02 15:04:05.000"),
		// The route reached the compression candidate path; counted whether or not
		// bytes shrank, so the CLI can tell "routing never applied" from "nothing
		// to compress" (the generic route records the same denominator).
		CompressionEligible:      compressionEligible,
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
		CacheCreation1hTokens:    usage.CacheCreation1hTokens,
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
	}
	meta := providers.RequestMetadata{Provider: "chatgpt-subscription", Model: model}
	if originalLogicalBody != nil {
		headersForInspect := r.Header.Clone()
		headersForInspect.Del("Content-Encoding")
		if inspected, err := openai.New("").InspectRequest(r.Context(), bytes.NewReader(originalLogicalBody), headersForInspect); err == nil {
			meta = inspected
			meta.Provider = "chatgpt-subscription"
		}
	}
	if s.chatGPTUpstream != DefaultChatGPTUpstream {
		meta.PricingUnsupportedReason = "custom_subscription_origin"
	}
	var acceptedLogicalBody []byte
	if acceptedBody == nil {
		acceptedLogicalBody = originalLogicalBody
	} else if decoded, _, ok := decodeChatGPTRequestBody(acceptedBody, r.Header.Get("Content-Encoding")); ok {
		acceptedLogicalBody = decoded
	}
	requestAccounting(&row, meta, usage, originalLogicalBody, acceptedLogicalBody, false)
	s.sink.Record(row)
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

func chatGPTRequestWantsStream(wire []byte, contentEncoding string, truncated, complete bool) bool {
	if truncated || !complete {
		return false
	}
	decoded, _, ok := decodeChatGPTRequestBody(wire, contentEncoding)
	if !ok {
		return false
	}
	var request struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(decoded, &request) == nil && request.Stream
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

const chatGPTSSEEventLimit = 4 << 20

// chatGPTSSECompletionReader keeps streaming response bytes unchanged while
// confirming that Pi receives a terminal Responses event. Pi's SSE parser only
// dispatches events terminated by a blank line, so a complete terminal JSON
// event at EOF gets the missing delimiter. An EOF without a terminal event is a
// truncated response, even when the HTTP body closed cleanly.
type chatGPTSSECompletionReader struct {
	source        io.Reader
	event         []byte
	eventOverflow bool
	eventTypeHint bool
	terminal      bool
	delivered     bool
	eof           bool
	ending        []byte
	endingErr     error
}

func (r *chatGPTSSECompletionReader) markClientWrite() {
	if r.terminal {
		r.delivered = true
	}
}

func (r *chatGPTSSECompletionReader) terminalDelivered() bool {
	return r.delivered
}

func isChatGPTEventStream(header http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && strings.EqualFold(mediaType, "text/event-stream")
}

func (r *chatGPTSSECompletionReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.ending) > 0 {
		n := copy(p, r.ending)
		r.ending = r.ending[n:]
		return n, nil
	}
	if r.eof {
		if r.endingErr != nil {
			err := r.endingErr
			r.endingErr = nil
			return 0, err
		}
		return 0, io.EOF
	}

	n, err := r.source.Read(p)
	if n > 0 {
		r.scan(p[:n])
	}
	if err == nil {
		return n, nil
	}
	if err != io.EOF {
		return n, err
	}

	r.eof = true
	if !r.terminal {
		if r.finalEventIsTerminal() {
			r.terminal = true
			if len(r.event) > 0 && r.event[len(r.event)-1] == '\n' {
				r.ending = []byte{'\n'}
			} else {
				r.ending = []byte{'\n', '\n'}
			}
		} else {
			r.endingErr = io.ErrUnexpectedEOF
		}
	}
	if n > 0 {
		return n, nil
	}
	if len(r.ending) > 0 || r.endingErr != nil {
		return r.Read(p)
	}
	return 0, io.EOF
}

func (r *chatGPTSSECompletionReader) scan(p []byte) {
	for len(p) > 0 {
		if r.eventOverflow {
			if len(r.event) > 0 && r.event[len(r.event)-1] == '\n' && p[0] == '\n' {
				r.finishEvent()
				p = p[1:]
				continue
			}
			if i := bytes.Index(p, []byte("\n\n")); i >= 0 {
				r.finishEvent()
				p = p[i+2:]
				continue
			}
			if len(p) > 0 && p[len(p)-1] == '\n' {
				r.event = []byte{'\n'}
			} else {
				r.event = nil
			}
			return
		}

		if len(r.event) > 0 && r.event[len(r.event)-1] == '\n' && p[0] == '\n' {
			r.finishEvent()
			p = p[1:]
			continue
		}
		if i := bytes.Index(p, []byte("\n\n")); i >= 0 {
			r.appendEvent(p[:i])
			r.finishEvent()
			p = p[i+2:]
			continue
		}
		r.appendEvent(p)
		return
	}
}

func (r *chatGPTSSECompletionReader) appendEvent(p []byte) {
	if len(p) == 0 {
		return
	}
	if len(p) > chatGPTSSEEventLimit-len(r.event) {
		combined := append(append([]byte(nil), r.event...), p...)
		r.eventTypeHint = chatGPTSSEPrefixIsTerminal(combined)
		r.eventOverflow = true
		if len(combined) > 0 && combined[len(combined)-1] == '\n' {
			r.event = []byte{'\n'}
		} else {
			r.event = nil
		}
		return
	}
	r.event = append(r.event, p...)
}

func (r *chatGPTSSECompletionReader) finishEvent() {
	if r.eventTypeHint || (!r.eventOverflow && chatGPTSSEEventIsTerminal(r.event)) {
		r.terminal = true
	}
	r.event = nil
	r.eventOverflow = false
	r.eventTypeHint = false
}

func (r *chatGPTSSECompletionReader) finalEventIsTerminal() bool {
	return !r.eventOverflow && chatGPTSSEEventIsTerminal(r.event)
}

func chatGPTSSEEventIsTerminal(frame []byte) bool {
	var data []byte
	for _, line := range bytes.Split(frame, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if bytes.HasPrefix(line, []byte("data:")) {
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, bytes.TrimSpace(line[len("data:"):])...)
		}
	}
	if len(data) == 0 {
		return false
	}
	var event struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(data, &event) == nil && terminalChatGPTResponseEvent(event.Type)
}

func chatGPTSSEPrefixIsTerminal(frame []byte) bool {
	var data []byte
	for _, line := range bytes.Split(frame, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if bytes.HasPrefix(line, []byte("data:")) {
			data = bytes.TrimSpace(line[len("data:"):])
			break
		}
	}
	if len(data) == 0 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	key, err := decoder.Token()
	if err != nil || key != "type" {
		return false
	}
	typeToken, err := decoder.Token()
	typeName, ok := typeToken.(string)
	return err == nil && ok && terminalChatGPTResponseEvent(typeName)
}

func terminalChatGPTResponseEvent(eventType string) bool {
	switch eventType {
	case "response.completed", "response.incomplete", "response.done", "response.failed", "error":
		return true
	default:
		return false
	}
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
