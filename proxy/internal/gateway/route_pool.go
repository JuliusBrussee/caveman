package gateway

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/internal/translate"
	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// RouteTarget is where a pool answer sends a request that leaves the
// harness's own provider and credential (contracts route-ask-v1 pool[]):
//   - via "local": straight to another provider on a login the person added
//     (`caveman providers add|login`), in the host's grammar (Wire), translated
//     there and back when it is not the caller's. Caveman Cloud never sees it.
//   - via "cloud": in the caller's grammar to the Caveman Cloud gateway, on
//     the link's credential, with x-caveman-route naming the pool entry. This
//     request DOES pass through Caveman Cloud.
//
// Any failure before the first byte reaches the agent falls back to the asked
// model on the harness's own credential.
type RouteTarget struct {
	PoolID string
	Via    string // local | cloud
	Host   string // the provider id ("cloud" for via cloud)
	Model  string // the catalog model name Cloud answered
	Wire   string // the upstream's grammar (translate.Messages|Chat|Responses)
	URL    string
	// Header carries the credential and the host's own headers.
	Header http.Header
	// Forward names caller headers copied as they came (a host whose terms
	// want the agent's own User-Agent).
	Forward []string
	// Affinity names a header carrying a stable per-session key.
	Affinity string
	// Translate is the host's translator options; Shown and Effort are set here.
	Translate translate.Options
}

// grammarOf is the wire grammar of a routable endpoint.
func grammarOf(endpoint string) string {
	switch {
	case strings.HasSuffix(endpoint, "/responses"):
		return translate.Responses
	case strings.HasSuffix(endpoint, "/chat/completions"):
		return translate.Chat
	}
	return translate.Messages
}

// affinityKey is the stable value a host's session-affinity header carries: a
// hash, so the session id itself never reaches the host.
func affinityKey(session string) string {
	sum := sha256.Sum256([]byte("caveman-affinity\x00" + session))
	return fmt.Sprintf("%x", sum[:16])
}

// targetResult is what one pool send did.
type targetResult struct {
	served bool // bytes reached the agent: the request is done, success or a cut stream
	usage  providers.UsageObservation
	bytes  int64
	errMsg string // a cut stream once served; why it fell back otherwise
}

// serveTarget sends body (the caller's grammar) to target at effort and, when
// the target answers 2xx, writes its answer to w in the caller's grammar,
// naming asked as the model. Nothing reaches w on any failure before that.
func (s *Server) serveTarget(w http.ResponseWriter, r *http.Request, adapter providers.Adapter, run *routeRun, endpoint string, body []byte, target *RouteTarget, effort, asked string) targetResult {
	grammar := grammarOf(endpoint)
	var payload []byte
	var reply *translate.Reply
	if target.Via == "cloud" {
		payload = withTopEffort(grammar, body, effort)
	} else {
		opts := target.Translate
		opts.Shown, opts.Effort = asked, effort
		out, translated, err := translate.Request(grammar, target.Wire, body, opts)
		if err != nil {
			return targetResult{errMsg: "pool_translate_failed"}
		}
		payload, reply = out, translated
	}
	header := target.Header.Clone()
	header.Set("content-type", "application/json")
	if target.Wire == translate.Messages {
		version := "2023-06-01"
		if grammar == translate.Messages && r.Header.Get("anthropic-version") != "" {
			version = r.Header.Get("anthropic-version")
		}
		header.Set("anthropic-version", version)
		if grammar == translate.Messages && (target.Via == "cloud" || target.Host == "anthropic") && r.Header.Get("anthropic-beta") != "" {
			header.Set("anthropic-beta", r.Header.Get("anthropic-beta"))
		}
	}
	for _, name := range target.Forward {
		if value := r.Header.Get(name); value != "" {
			header.Set(name, value)
		}
	}
	if target.Affinity != "" && run != nil && run.key != "" {
		header.Set(target.Affinity, affinityKey(run.key))
	}
	resp, err := s.doUpstream(r.Context(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.URL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header = header.Clone()
		return req, nil
	})
	if err != nil {
		return targetResult{errMsg: "pool_unreachable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return targetResult{errMsg: fmt.Sprintf("pool_%d", resp.StatusCode)}
	}
	if reply != nil {
		counter := &countingWriter{w: w}
		usage, err := reply.Serve(&countedResponse{ResponseWriter: w, counter: counter}, resp)
		out := targetResult{served: true, bytes: counter.n, usage: providers.UsageObservation{
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
			CachedInputTokens: usage.CacheReadTokens, CacheCreationInputTokens: usage.CacheWriteTokens, CacheStatus: "unknown",
		}}
		if err != nil {
			out.errMsg = "cave_upstream_body_read_failed"
		}
		return out
	}
	// via cloud: the caller's grammar both ways; the agent reads the model it asked for.
	stream := streamingResponse(resp.Header)
	copySafeResponseHeaders(w.Header(), resp.Header)
	w.Header().Del("Content-Length")
	w.WriteHeader(resp.StatusCode)
	scanner := adapter.NewUsageScanner(resp.Header)
	src := io.Reader(newShownModel(io.TeeReader(resp.Body, scanner), target.Model, asked))
	counter, copyErr := s.streamResponse(w, r, src, stream, "")
	return targetResult{served: true, bytes: counter.n, usage: scanner.Usage(), errMsg: copyErr}
}

// countedResponse counts what a translator writes to the agent.
type countedResponse struct {
	http.ResponseWriter
	counter *countingWriter
}

func (c *countedResponse) Write(p []byte) (int, error) { return c.counter.Write(p) }

func (c *countedResponse) Flush() { _ = http.NewResponseController(c.ResponseWriter).Flush() }

// withTopEffort sets a top-level effort in the caller's own grammar ("" keeps
// the body as it is).
func withTopEffort(grammar string, body []byte, effort string) []byte {
	if effort == "" {
		return body
	}
	path := []string{"output_config", "effort"}
	switch grammar {
	case translate.Responses:
		path = []string{"reasoning", "effort"}
	case translate.Chat:
		path = []string{"reasoning_effort"}
	}
	out, _ := setString(body, effort, path...)
	return out
}

// nativeHistory removes from a request bound for the harness's own provider
// the reasoning another host produced in this conversation (a pool request's
// translated thinking, or an encrypted envelope only this runtime reads),
// which Anthropic and OpenAI refuse. A body that carries none goes byte for
// byte.
func nativeHistory(provider, endpoint string, body []byte) []byte {
	switch {
	case provider == "anthropic" && strings.HasSuffix(endpoint, "/messages"),
		provider == "anthropic" && strings.HasSuffix(endpoint, "/messages/count_tokens"):
		return translate.AnthropicNative(body)
	case provider == "openai" && strings.HasSuffix(endpoint, "/responses"):
		return translate.OpenAINative(body)
	}
	return body
}
