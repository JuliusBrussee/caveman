package gateway

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"github.com/JuliusBrussee/caveman/shared/platform/httpx"
	"github.com/klauspost/compress/zstd"
)

// Auto on a ChatGPT login (Codex, OpenCode): the model catalog Codex reads
// at /chatgpt/models gains an Auto entry while Auto is offered, and a
// /responses request naming Auto gets the route stage on the ChatGPT
// backend's own login, among AutoOpenAIModels only: the ask lists those and no
// pool. Every other POST naming
// Auto (Codex compaction, any other path) runs gpt-6.1-sol unrouted; the
// literal id never goes upstream.

// autoOfferer is a CloudLink that says whether Auto is offered: signed in
// with the routing module on.
type autoOfferer interface{ AutoOffered() bool }

func (s *Server) autoOffered() bool {
	offerer, ok := s.cloud.(autoOfferer)
	return ok && offerer.AutoOffered()
}

// chatGPTAutoModels forwards GET /models and appends Auto to the catalog.
// The answer is asked for uncompressed and without a conditional: a cached
// catalog must follow Auto being offered or not. Anything unreadable goes
// to the agent as the backend sent it.
func (s *Server) chatGPTAutoModels(w http.ResponseWriter, r *http.Request, upstreamURL string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "cave_provider_request_invalid", "ChatGPT route could not build the upstream request.")
		return
	}
	req.Header = chatGPTRequestHeaders(r.Header)
	req.Header.Del("accept-encoding")
	req.Header.Del("if-none-match")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unreachable", "ChatGPT upstream is unreachable.")
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_body_read_failed", "Upstream response could not be read completely.")
		return
	}
	if resp.StatusCode == http.StatusOK && resp.Header.Get("Content-Encoding") == "" {
		if out, ok := withAutoModel(body, autoFallback["openai"]); ok {
			body = out
			resp.Header.Del("ETag")
		}
	}
	copySafeResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// withAutoModel appends Auto to a Codex model catalog ({"models":[…]}): a copy
// of the fallback model's entry, so the picker shows that model's reasoning
// efforts and context window, named Auto, listed last. ok is false when the
// catalog has no such entry, already lists Auto, or is not one.
func withAutoModel(catalog []byte, fallback string) ([]byte, bool) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(catalog, &doc) != nil {
		return nil, false
	}
	var models []json.RawMessage
	if json.Unmarshal(doc["models"], &models) != nil {
		return nil, false
	}
	var auto map[string]any
	priority := 0.0
	for _, raw := range models {
		var entry map[string]any
		if json.Unmarshal(raw, &entry) != nil {
			return nil, false
		}
		switch entry["slug"] {
		case AutoModel:
			return nil, false
		case fallback:
			auto = entry
		}
		if p, ok := entry["priority"].(float64); ok && p > priority {
			priority = p
		}
	}
	if auto == nil {
		return nil, false
	}
	auto["slug"], auto["display_name"], auto["description"], auto["priority"] = AutoModel, autoName, autoDescription, priority+1
	delete(auto, "upgrade")
	delete(auto, "availability_nux")
	entry, err := json.Marshal(auto)
	if err != nil {
		return nil, false
	}
	if doc["models"], err = json.Marshal(append(models, entry)); err != nil {
		return nil, false
	}
	out, err := json.Marshal(doc)
	return out, err == nil
}

// The picker copy, as the CLI writes it for the other agents
// (packages/cli/src/modules/registry.ts).
const (
	autoName        = "Auto"
	autoDescription = "Caveman pick model + effort each turn. Hard ask, big brain. Easy ask, save rocks."
)

// chatGPTAutoBody returns the request body when it names Auto, decoded when
// the agent compressed it (Codex sends zstd). tooLarge is a body that decodes
// past the limit; any other failure is a body that does not decode.
func chatGPTAutoBody(captured []byte, contentEncoding string) (body []byte, tooLarge, ok bool) {
	const limit = 32 << 20
	reader, ok := decodedReader(bytes.NewReader(captured), contentEncoding)
	if !ok {
		return nil, false, false
	}
	defer reader.Close()
	decoded, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || len(decoded) > limit {
		return nil, err == nil, false
	}
	return decoded, false, namesAuto(decoded)
}

// chatGPTAuto serves one POST naming Auto on the agent's own login: a turn
// (/responses) runs the model and effort Cloud picks among AutoOpenAIModels,
// anything else gpt-6.1-sol. A 4xx on bytes
// the route stage changed replays the fallback model's own bytes. The agent
// reads caveman-auto as the model.
func (s *Server) chatGPTAuto(w http.ResponseWriter, r *http.Request, rc RequestContext, requestID, traceID, upstreamURL, suffix string, start time.Time, evidence requestEvidence, body []byte) {
	fallback := autoFallback["openai"]
	asked, ok := setModel(body, fallback)
	if !ok {
		httpx.Error(w, r, http.StatusBadRequest, "cave_auto_unavailable", "Auto needs a readable model field.")
		return
	}
	// Only a turn is routed; compaction and other requests run the fallback.
	turn := suffix == "/responses"
	route := RouteAnswer{Outcome: "off", Reason: "not_a_turn"}
	if turn {
		route.Reason = ""
	}
	adapter := openai.New(s.chatGPTUpstream)
	inspectHeader := r.Header.Clone()
	inspectHeader.Set("x-cave-route-path", "/responses")
	meta, inspectErr := adapter.InspectRequest(r.Context(), bytes.NewReader(asked), inspectHeader)
	meta.Endpoint, meta.SessionID = "/responses", evidence.SessionID
	// The ask starts first, so its round trip overlaps compression.
	var run *routeRun
	var await func() RouteAnswer
	if s.cloud != nil && turn {
		exact := ""
		if evidence.SessionCorrelationBasis == "explicit_header" {
			exact = evidence.SessionID
		}
		run = newRouteRun(r.Header, exact, "/v1/responses", asked)
		run.asked = fallback
		last, perMessageOff := s.routes.facts(run.key, time.Now())
		await = s.cloud.Ask(r.Context(), RouteAsk{
			Provider: "openai", Endpoint: "/v1/responses", Model: fallback, Agent: rc.AgentSlug,
			SessionID: run.key, ParentSessionID: run.parent, ToolsCount: meta.ToolsCount, InputBytes: len(asked), Body: asked,
			Labels: run.labels, PerRequest: run.perRequest, Last: last, PerMessageOff: perMessageOff, Models: AutoOpenAIModels, NoPool: true,
		})
	}
	// The same live-zone compression the route gives any other request.
	transform := providers.TransformResult{Body: asked, OptimizerIDs: []string{}}
	var comp *compressionOutcome
	lockedRoutes, planAllowed := compiledPlanRoutes(r.Header)
	eligible := turn && rc.RuntimeMode == "compress" && s.compressor != nil && s.liveZoneCompressionAllowed(adapter, nil) && planAllowed && inspectErr == nil
	if eligible && s.cacheEpochAllows(r, adapter, meta, asked, evidence.SessionID) {
		comp = s.compressRequest(adapter, asked, meta, &transform, requestID, lockedRoutes)
	}
	sent, model := transform.Body, fallback
	if await != nil {
		// AutoOpenAIModels only, never a pool entry: anything else runs
		// gpt-6.1-sol at that answer's effort.
		route = autoOpenAIAnswer(await(), "/v1/responses", fallback, sent, true)
		if route.Model != "" && route.Model != fallback {
			if moved, ok := setModel(sent, route.Model); ok {
				sent, model = moved, route.Model
			}
		}
		sent = s.applyEffort(run, "openai", "/v1/responses", model, sent, route)
	}
	header := chatGPTRequestHeaders(r.Header)
	for _, name := range []string{"Content-Length", "Content-Encoding", "accept-encoding"} {
		header.Del(name) // the body goes decoded; the answer comes back identity for its model rewrite
	}
	send := func(payload []byte) (*http.Response, error) {
		return s.doUpstream(r.Context(), func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			req.Header = header.Clone()
			return req, nil
		})
	}
	resp, err := send(sent)
	if err == nil && resp.StatusCode >= 400 && resp.StatusCode < 500 && !bytes.Equal(sent, asked) {
		// The login refused the routed model, its effort or the compressed
		// bytes: the fallback model's own bytes, and the rest of this ask
		// stays there.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if route.Reject != nil {
			route.Reject()
		}
		route = RouteAnswer{Outcome: "degraded", Reason: "provider_rejected_routed_model", DecisionID: route.DecisionID}
		sent, model, comp = asked, fallback, nil
		transform.OptimizerIDs = []string{}
		if run != nil {
			run.effort = ""
		}
		resp, err = send(asked)
	}
	reqCapture := &cappedBuffer{limit: chatGPTCaptureLimit}
	_, _ = reqCapture.Write(sent)
	rawHash := sha256.Sum256(body)
	sentHash := sha256.Sum256(sent)
	record := func(status int, errCode string, respCapture *cappedBuffer, respBytes int64, stream bool) {
		s.recordChatGPT(rc, r, requestID, traceID, suffix, start, status, errCode, reqCapture, rawHash[:], sentHash[:], true, respCapture, respBytes, stream, transform.OptimizerIDs, comp, eligible, sent,
			&chatGPTRoute{from: AutoModel, answer: route})
	}
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unreachable", "ChatGPT upstream is unreachable.")
		record(0, "cave_upstream_unreachable", nil, 0, false)
		return
	}
	defer resp.Body.Close()
	stream := streamingResponse(resp.Header)
	identity := resp.Header.Get("Content-Encoding") == "" || strings.EqualFold(resp.Header.Get("Content-Encoding"), "identity")
	// The capture reads the provider's own bytes: usage and last come from them.
	respCapture := &cappedBuffer{limit: chatGPTCaptureLimit}
	var served servedModel
	src := io.Reader(io.TeeReader(resp.Body, io.MultiWriter(respCapture, &served)))
	switch {
	case !identity: // left as it is, like a compressed answer on the provider path
	case stream:
		resp.Header.Del("Content-Length")
		src = newShownModel(src, model, AutoModel)
	default:
		data, rerr := io.ReadAll(src)
		if rerr != nil {
			httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_body_read_failed", "Upstream response could not be read completely.")
			record(0, "cave_upstream_body_read_failed", nil, 0, false)
			return
		}
		if shown, ok := setModel(data, AutoModel); ok {
			data = shown
		}
		resp.Header.Set("Content-Length", strconv.Itoa(len(data)))
		src = bytes.NewReader(data)
	}
	copySafeResponseHeaders(w.Header(), resp.Header)
	if comp != nil {
		w.Header().Set("x-cave-mode", rc.RuntimeMode)
		w.Header().Set("x-cave-optimization", strings.Join(transform.OptimizerIDs, ","))
		w.Header().Set("x-caveman-recovery-handle", comp.handle)
	}
	if model != fallback {
		w.Header().Set("x-caveman-routed-from", AutoModel)
	}
	w.WriteHeader(resp.StatusCode)
	counter, errCode := s.streamResponse(w, r, src, stream, requestID)
	record(resp.StatusCode, errCode, respCapture, counter.n, stream)
	if run != nil && !run.auxiliary && run.key != "" && resp.StatusCode < 300 && errCode == "" && !respCapture.truncated {
		var usage providers.UsageObservation
		providers.ParseUsageBytes("openai", respCapture.buf.Bytes(), &usage)
		if run.effort == "" {
			run.effort = effortInForce("/v1/responses", sent)
		}
		s.routes.served(run.key, RouteLast{
			Model: labelOrDefault(served.name(""), model), Effort: run.effort,
			InputTokens: usage.InputTokens, CacheReadTokens: usage.CachedInputTokens, Compacted: run.compacted,
		}, time.Now(), model != fallback && !run.perRequest)
	}
	if errCode != "" {
		panic(http.ErrAbortHandler)
	}
}

// autoSniffBytes is how much of a /chatgpt POST is read to tell whether it
// names Auto; the rest streams as before when it does not.
const autoSniffBytes = 64 << 10

// autoSniffRaw is how much of the body as sent is read for that: one whole
// zstd block (128 KiB at most) and its frame header, so an encoded head
// always decodes far enough to show its model.
const autoSniffRaw = 192 << 10

// prefixModel spans the value of the top-level "model" string in the head of
// a JSON object (quotes included). ok is false when the head ends first or
// holds no such string.
func prefixModel(b []byte) (start, end int, ok bool) {
	stringEnd := func(i int) int { // index past the string opening at i, -1 when cut
		for j := i + 1; j < len(b); j++ {
			switch b[j] {
			case '\\':
				j++
			case '"':
				return j + 1
			}
		}
		return -1
	}
	space := func(i int) int {
		for i < len(b) && strings.IndexByte(" \t\r\n", b[i]) >= 0 {
			i++
		}
		return i
	}
	depth := 0
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '"':
			end := stringEnd(i)
			if end < 0 {
				return 0, 0, false
			}
			if colon := space(end); depth == 1 && string(b[i:end]) == `"model"` && colon < len(b) && b[colon] == ':' {
				if value := space(colon + 1); value < len(b) && b[value] == '"' {
					if valueEnd := stringEnd(value); valueEnd > 0 {
						return value, valueEnd, true
					}
				}
				return 0, 0, false
			}
			i = end - 1
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}
	return 0, 0, false
}

// decodedReader decodes a request body as it streams; ok is false for an
// encoding with no decoder here (br): such a body is never looked into. The
// caller closes the reader, which releases the decoder and leaves body open.
func decodedReader(body io.Reader, encoding string) (io.ReadCloser, bool) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return io.NopCloser(body), true
	case "gzip", "x-gzip":
		reader, err := gzip.NewReader(body)
		if err != nil {
			return nil, false
		}
		return reader, true
	case "deflate":
		// "deflate" is zlib by the RFC, but some clients send the bare
		// stream: no valid zlib header means raw deflate.
		buffered := bufio.NewReader(body)
		if h, _ := buffered.Peek(2); len(h) == 2 && h[0]&0x0f == 8 && (uint(h[0])<<8|uint(h[1]))%31 == 0 {
			reader, err := zlib.NewReader(buffered)
			if err != nil {
				return nil, false
			}
			return reader, true
		}
		return flate.NewReader(buffered), true
	case "zstd":
		// One goroutine-free decoder: the default starts stream goroutines
		// that only Close stops.
		reader, err := zstd.NewReader(body, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, false
		}
		return reader.IOReadCloser(), true
	}
	return nil, false
}

// upstreamBody is a decoded request body handed to the HTTP transport, which
// may close it from another goroutine while a Read is still in flight; the
// decoders are not safe for that, so a Close during a Read waits for it.
type upstreamBody struct {
	io.Reader
	decoder io.Closer

	mu              sync.Mutex
	reading, closed bool
}

func (b *upstreamBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	b.reading = true
	b.mu.Unlock()
	n, err := b.Reader.Read(p)
	b.mu.Lock()
	b.reading = false
	if b.closed {
		_ = b.decoder.Close()
	}
	b.mu.Unlock()
	return n, err
}

func (b *upstreamBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	if b.reading {
		return nil // the Read in flight closes the decoder on its way out
	}
	return b.decoder.Close()
}

// namesAutoHead reports a POST body whose head names Auto as its model.
func namesAutoHead(head []byte, encoding string) bool {
	reader, ok := decodedReader(bytes.NewReader(head), encoding)
	if !ok {
		return false
	}
	defer reader.Close()
	decoded, _ := io.ReadAll(io.LimitReader(reader, autoSniffBytes)) // a cut stream still yields its head
	start, end, ok := prefixModel(decoded)
	return ok && string(decoded[start:end]) == `"`+AutoModel+`"`
}

// chatGPTAutoDoor serves a /chatgpt POST whose head names Auto. A body that
// fits CAVE_MAX_REQUEST_BYTES and decodes whole goes through chatGPTAuto; a
// bigger one streams on gpt-6.1-sol, unrouted, with only its model changed.
// The turn never fails for its size, and the literal id never goes upstream.
func (s *Server) chatGPTAutoDoor(w http.ResponseWriter, r *http.Request, rc RequestContext, requestID, traceID, upstreamURL, suffix string, start time.Time, evidence requestEvidence) {
	encoding := r.Header.Get("Content-Encoding")
	maxBytes := env.Int("CAVE_MAX_REQUEST_BYTES", 33554432)
	raw, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBytes)+1))
	reason := "auto_body_too_large"
	if err == nil && len(raw) <= maxBytes {
		body, tooLarge, ok := chatGPTAutoBody(raw, encoding)
		if ok {
			s.chatGPTAuto(w, r, rc, requestID, traceID, upstreamURL, suffix, start, evidence, body)
			return
		}
		if !tooLarge {
			reason = "auto_body_decode_failed"
		}
	}
	unreadable := func() {
		httpx.Error(w, r, http.StatusBadRequest, "cave_auto_unavailable", "Caveman could not read this Auto request; pick a model and retry.")
	}
	decoded, ok := decodedReader(io.MultiReader(bytes.NewReader(raw), r.Body), encoding)
	if !ok {
		unreadable()
		return
	}
	// The transport closes the body it is given; this covers every return
	// before that and an upstream abort.
	upstream := &upstreamBody{decoder: decoded}
	defer upstream.Close()
	head, _ := io.ReadAll(io.LimitReader(decoded, autoSniffBytes))
	from, to, ok := prefixModel(head)
	if !ok {
		unreadable()
		return
	}
	fallback := autoFallback["openai"]
	sent := append(append(append([]byte(nil), head[:from]...), `"`+fallback+`"`...), head[to:]...)
	upstream.Reader = io.MultiReader(bytes.NewReader(sent), decoded)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, upstream)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "cave_provider_request_invalid", "ChatGPT route could not build the upstream request.")
		return
	}
	req.Header = chatGPTRequestHeaders(r.Header)
	for _, name := range []string{"Content-Length", "Content-Encoding", "accept-encoding"} {
		req.Header.Del(name)
	}
	req.ContentLength = -1
	reqCapture := &cappedBuffer{limit: chatGPTCaptureLimit}
	_, _ = reqCapture.Write(sent)
	reqCapture.truncated = true // the rest streamed: no model read back, no hash
	route := &chatGPTRoute{from: AutoModel, answer: RouteAnswer{Outcome: "off", Reason: reason}}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "cave_upstream_unreachable", "ChatGPT upstream is unreachable.")
		s.recordChatGPT(rc, r, requestID, traceID, suffix, start, 0, "cave_upstream_unreachable", reqCapture, nil, nil, false, nil, 0, false, nil, nil, false, nil, route)
		return
	}
	defer resp.Body.Close()
	stream := streamingResponse(resp.Header)
	src := io.Reader(resp.Body)
	if encoding := resp.Header.Get("Content-Encoding"); encoding == "" || strings.EqualFold(encoding, "identity") {
		resp.Header.Del("Content-Length")
		src = newShownModel(resp.Body, fallback, AutoModel)
	}
	copySafeResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	counter, errCode := s.streamResponse(w, r, src, stream, requestID)
	s.recordChatGPT(rc, r, requestID, traceID, suffix, start, resp.StatusCode, errCode, reqCapture, nil, nil, false, nil, counter.n, stream, nil, nil, false, nil, route)
	if errCode != "" {
		panic(http.ErrAbortHandler)
	}
}
