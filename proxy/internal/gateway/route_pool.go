package gateway

import (
	"bytes"
	"crypto/sha256"
	"errors"
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
	stream bool // the agent's answer streamed
	usage  providers.UsageObservation
	bytes  int64
	errMsg string // a cut stream once served; why it fell back otherwise
	// upstreamID is the host's own id for its answer (translate.Reply.UpstreamID).
	upstreamID string
}

// serveTarget sends body (the caller's grammar) to target at effort and, when
// the target answers 2xx, writes its answer to w in the caller's grammar,
// naming asked as the model. Nothing reaches w on any failure before that.
func (s *Server) serveTarget(w http.ResponseWriter, r *http.Request, run *routeRun, harnessKey, endpoint string, body []byte, target *RouteTarget, effort, asked string) targetResult {
	grammar := grammarOf(endpoint)
	var payload []byte
	var reply *translate.Reply
	if target.Via == "cloud" {
		payload = body // as the agent sent it: x-caveman-route decides the model and effort
		reply = translate.Relay(grammar, body, asked)
	} else {
		opts := target.Translate
		opts.Shown, opts.Effort = asked, effort
		if opts.Route == "openai" && target.Wire == translate.Responses && target.Header.Get("authorization") != "Bearer "+harnessKey {
			// Another OpenAI login (another organisation, maybe): its encrypted
			// reasoning is tagged so the harness's own key never gets it back.
			opts.Route = "openai-login"
		}
		out, translated, err := translateRequest(grammar, target.Wire, body, opts)
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
		if grammar == translate.Messages && (target.Via == "cloud" || target.Host == "anthropic") {
			if betas := withoutOAuthBetas(r.Header.Values("anthropic-beta")); betas != "" {
				header.Set("anthropic-beta", betas)
			}
		}
	}
	for _, name := range target.Forward {
		if value := r.Header.Get(name); value != "" {
			header.Set(name, value)
		}
	}
	if target.Via == "cloud" && effort != "" {
		header.Set("x-caveman-effort", effort) // the gateway applies it in the target's own shape
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
	counter := &countingWriter{w: w}
	usage, err := reply.Serve(&countedResponse{ResponseWriter: w, counter: counter}, resp)
	if errors.Is(err, translate.ErrNotServed) {
		// A 2xx that failed or ended before any content: nothing reached the
		// agent, so the asked model still runs.
		return targetResult{errMsg: "pool_failed_before_content", upstreamID: reply.UpstreamID()}
	}
	out := targetResult{served: true, stream: reply.Stream(), bytes: counter.n, upstreamID: reply.UpstreamID(), usage: providers.UsageObservation{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CachedInputTokens: usage.CacheReadTokens, CacheCreationInputTokens: usage.CacheWriteTokens, CacheStatus: "unknown",
	}}
	switch {
	case errors.Is(err, translate.ErrUpstreamFailed):
		out.errMsg = "pool_upstream_failed" // the host's own failure, relayed as its stream's end
	case err != nil:
		out.errMsg = "cave_upstream_body_read_failed"
	}
	return out
}

// translateRequest is translate.Request with a panic in the ported parsers
// turned into a refusal: nothing has reached the agent yet, so the request
// falls back to the asked model instead of losing its connection.
func translateRequest(from, to string, body []byte, opts translate.Options) (out []byte, reply *translate.Reply, err error) {
	defer func() {
		if recover() != nil {
			out, reply, err = nil, nil, errors.New("translator panic")
		}
	}()
	return translate.Request(from, to, body, opts)
}

// countedResponse counts what a translator writes to the agent.
type countedResponse struct {
	http.ResponseWriter
	counter *countingWriter
}

func (c *countedResponse) Write(p []byte) (int, error) { return c.counter.Write(p) }

func (c *countedResponse) Flush() { _ = http.NewResponseController(c.ResponseWriter).Flush() }

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
	case strings.HasSuffix(endpoint, "/chat/completions"):
		return translate.ChatNative(body) // no chat API takes another host's reasoning
	}
	return body
}

// withoutOAuthBetas joins the agent's betas less the oauth-* ones: those
// belong to the subscription login, and a pool target runs on another
// credential.
func withoutOAuthBetas(values []string) string {
	var kept []string
	for _, beta := range strings.Split(strings.Join(values, ","), ",") {
		if beta = strings.TrimSpace(beta); beta != "" && !strings.HasPrefix(strings.ToLower(beta), "oauth-") {
			kept = append(kept, beta)
		}
	}
	return strings.Join(kept, ",")
}
