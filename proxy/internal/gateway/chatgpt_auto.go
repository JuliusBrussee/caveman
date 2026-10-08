package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/shared/platform/httpx"
)

// Auto on a ChatGPT login (Codex, OpenCode): the model catalog Codex reads
// at /chatgpt/models gains an Auto entry while Auto is offered, and a
// /responses request naming Auto gets the route stage on the ChatGPT
// backend's own models, on the agent's own login. Nothing here leaves for
// another host: the ask carries no pool, so the answer is a model and an
// effort, never a pool target.

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
// the agent compressed it (Codex sends zstd).
func chatGPTAutoBody(captured []byte, contentEncoding string) ([]byte, bool) {
	if contentEncoding = strings.TrimSpace(contentEncoding); contentEncoding != "" && !strings.EqualFold(contentEncoding, "identity") {
		decoded, ok := providers.DecodeBody(captured, contentEncoding, 32<<20)
		if !ok {
			return nil, false
		}
		captured = decoded
	}
	return captured, namesAuto(captured)
}

// chatGPTAuto serves one /responses request naming Auto: it runs on the
// fallback model unless the route stage moves it to another model of the
// ChatGPT backend, at the answered effort, on the agent's own login. A 4xx
// on bytes the route stage changed replays the fallback model's bytes. The
// agent reads caveman-auto as the model.
func (s *Server) chatGPTAuto(w http.ResponseWriter, r *http.Request, rc RequestContext, requestID, traceID, upstreamURL string, start time.Time, evidence requestEvidence, body []byte) {
	fallback := autoFallback["openai"]
	asked, ok := setModel(body, fallback)
	if !ok {
		httpx.Error(w, r, http.StatusBadRequest, "cave_auto_unavailable", "Auto needs a readable model field.")
		return
	}
	route := RouteAnswer{Outcome: "off"}
	adapter := openai.New(s.chatGPTUpstream)
	inspectHeader := r.Header.Clone()
	inspectHeader.Set("x-cave-route-path", "/responses")
	meta, inspectErr := adapter.InspectRequest(r.Context(), bytes.NewReader(asked), inspectHeader)
	meta.Endpoint, meta.SessionID = "/responses", evidence.SessionID
	// The ask starts first, so its round trip overlaps compression.
	var run *routeRun
	var await func() RouteAnswer
	if s.cloud != nil {
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
			Labels: run.labels, PerRequest: run.perRequest, Last: last, PerMessageOff: perMessageOff, NoPool: true,
		})
	}
	// The same live-zone compression the route gives any other request.
	transform := providers.TransformResult{Body: asked, OptimizerIDs: []string{}}
	var comp *compressionOutcome
	lockedRoutes, planAllowed := compiledPlanRoutes(r.Header)
	eligible := rc.RuntimeMode == "compress" && s.compressor != nil && s.liveZoneCompressionAllowed(adapter, nil) && planAllowed && inspectErr == nil
	if eligible && s.cacheEpochAllows(r, adapter, meta, asked, evidence.SessionID) {
		comp = s.compressRequest(adapter, asked, meta, &transform, requestID, lockedRoutes)
	}
	sent, model := transform.Body, fallback
	if await != nil {
		route = await()
		if route.Target != nil { // never asked for; kept off the subscription all the same
			route = RouteAnswer{Outcome: "degraded", Reason: "pool_target_on_subscription", DecisionID: route.DecisionID}
		}
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
		// The login refused the routed model or effort: the fallback model's
		// own bytes, and the rest of this ask stays there.
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
		s.recordChatGPT(rc, r, requestID, traceID, "/responses", start, status, errCode, reqCapture, rawHash[:], sentHash[:], true, respCapture, respBytes, stream, transform.OptimizerIDs, comp, eligible, sent,
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
