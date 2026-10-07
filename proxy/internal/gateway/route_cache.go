package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/translate"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// Cache mechanics the route stage applies on its own: they keep a provider
// cache the session already paid for and decide nothing about models.
//   - prompt_cache_key: OpenAI routes a prefix to the machine holding it by
//     this key; a request without one gets its session's (hashed; one key a
//     session keeps each key under OpenAI's ~15 requests a minute).
//   - OpenRouter: x-session-id (RouteTarget.Affinity, the family's) makes it
//     sticky; once a
//     session's pool entry is warm on one of OpenRouter's providers it is
//     pinned there (provider.order + allow_fallbacks false), so an idle gap or
//     a provider error never spreads it to a cold one. A pinned request that
//     fails drops the pin: the next one is routed afresh.
//   - fan-out: sibling children starting on one new prefix go one first; the
//     rest wait for its first content (or fanoutWait), then read its cache write.
//   - context tokens: the session's own bytes-to-tokens ratio, from the
//     provider's count, a child taking its parent's.

// family is the session a host's affinity header keys on: its parent's for a
// child (siblings share their tools and system prompt, a forked child its
// parent's whole history, so all of them belong on one provider), else its
// own.
func (run *routeRun) family() string {
	if run.parent != "" {
		return run.parent
	}
	return run.key
}

// withCacheKey sets prompt_cache_key to the session's key when the body has
// none (the harness's own is never replaced).
func withCacheKey(body []byte, session string) []byte {
	root, ok := jsonsplice.Root(body)
	if !ok || session == "" {
		return body
	}
	if _, set := jsonsplice.Field(body, root, "prompt_cache_key"); set {
		return body
	}
	key, _ := json.Marshal(openai.SessionCacheKey(session))
	out, err := jsonsplice.AppendObjectFields(body, root, jsonsplice.FieldInsertion{Name: "prompt_cache_key", Value: key})
	if err != nil {
		return body
	}
	return out
}

// withCacheKey gives an OpenAI request of the session its key while the
// route stage is on for it (a stateful chain, answered off for a reason,
// included: a key that came and went would send follow-ups to a cold
// machine); routing off, signed out and record mode stay byte for byte. It
// goes in before the effort, so a marks heal (built from the unmarked body)
// keeps it, and the original-bytes retry keeps it too.
func (run *routeRun) withCacheKey(provider string, body []byte) []byte {
	if provider != "openai" || run.record {
		return body // record mode never transforms
	}
	keyed := withCacheKey(body, run.key)
	run.keyed = run.keyed || len(keyed) != len(body)
	return keyed
}

// withProviderPin pins an OpenRouter request to provider (the name its
// answers carry, which provider.order takes as is), unless the body names
// providers of its own.
func withProviderPin(body []byte, provider string) []byte {
	root, ok := jsonsplice.Root(body)
	if !ok || provider == "" {
		return body
	}
	if _, set := jsonsplice.Field(body, root, "provider"); set {
		return body
	}
	order, _ := json.Marshal([]string{provider})
	out, err := jsonsplice.AppendObjectFields(body, root, jsonsplice.FieldInsertion{Name: "provider", Value: []byte(`{"order":` + string(order) + `,"allow_fallbacks":false}`)})
	if err != nil {
		return body
	}
	return out
}

// pinned is the OpenRouter provider key's pool entry is warm on, "" none.
func (rs *routeSessions) pinned(key, pool string) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if session := rs.get(key, false); session != nil && session.pinPool == pool {
		return session.pinProvider
	}
	return ""
}

// pin records the provider key's pool entry is warm on; "" drops the pin.
// ponytail: one pin per session (its newest entry); a map if sessions
// alternate between OpenRouter models.
func (rs *routeSessions) pin(key, pool, provider string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if session := rs.get(key, provider != ""); session != nil {
		session.pinPool, session.pinProvider = pool, provider
	}
}

// tokensPerByte is the provider's input tokens per request byte in key's
// session, its parent's first (a forked child resends the parent's history,
// which Cloud prices in the parent model's tokens); 0 when neither has one.
func (rs *routeSessions) tokensPerByte(key, parent string) float64 {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, k := range []string{parent, key} {
		if session := rs.get(k, false); session != nil && session.last != nil && session.lastBytes > 0 && session.last.InputTokens > 0 {
			// Between 1 and 16 bytes a token; anything else is not a ratio.
			return min(max(float64(session.last.InputTokens)/float64(session.lastBytes), 1.0/16), 1)
		}
	}
	return 0
}

// openRouterProviderRE is the "provider" OpenRouter adds to its answers: in a
// stream's first event (message_start, a chat chunk), where tool input still
// streams as escaped strings that never match.
var openRouterProviderRE = regexp.MustCompile(`"provider"\s*:\s*"([^"\\]{1,64})"`)

// providerSniff keeps the start of an OpenRouter answer to read which of its
// providers served it.
type providerSniff struct {
	io.ReadCloser
	stream bool
	head   []byte
	total  int
}

func (p *providerSniff) Read(b []byte) (int, error) {
	n, err := p.ReadCloser.Read(b)
	limit := 2 << 20 // a whole JSON answer: its provider comes after the content
	if p.stream {
		limit = 64 << 10
	}
	if room := limit - len(p.head); room > 0 {
		p.head = append(p.head, b[:min(n, room)]...)
	}
	p.total += n
	return n, err
}

// provider is the name OpenRouter's answer gives its provider, "" none: the
// first one in a stream, the top-level field of a whole JSON answer.
func (p *providerSniff) provider() string {
	if p.stream {
		if match := openRouterProviderRE.FindSubmatch(p.head); match != nil {
			return string(match[1])
		}
		return ""
	}
	var answer struct {
		Provider string `json:"provider"`
	}
	if p.total > len(p.head) || json.Unmarshal(p.head, &answer) != nil || len(answer.Provider) > 64 {
		return ""
	}
	return answer.Provider
}

// fanout holds sibling children that start on the same new prefix: the first
// one goes, the others wait until its answer has content (the provider writes
// a prefix's cache entry before it answers), fanoutWait at most or until
// their own request is cancelled, then read that one write instead of each
// writing it again (a cache entry exists only once its first answer has
// begun, so N parallel requests on one new prefix write it N times). A
// prefix with content in the last fanoutWarm is warm: nobody waits on it.
type fanout struct {
	mu      sync.Mutex
	leaders map[[32]byte]chan struct{}
	warm    map[[32]byte]time.Time
}

// fanoutWait is how long a sibling waits at most (a var for tests).
var fanoutWait = 5 * time.Second

const (
	fanoutWarm = 5 * time.Minute
	fanoutMax  = 1024
)

// enter waits behind key's leader, or makes this request the leader. The
// release it returns (idempotent; a follower's does nothing) lets the
// followers go; ok marks the prefix warm (the leader got content in a
// 2xx answer).
func (f *fanout) enter(ctx context.Context, key [32]byte) (release func(ok bool)) {
	f.mu.Lock()
	if at, ok := f.warm[key]; ok && time.Since(at) < fanoutWarm {
		f.mu.Unlock()
		return func(bool) {}
	}
	if done, ok := f.leaders[key]; ok {
		f.mu.Unlock()
		timer := time.NewTimer(fanoutWait)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		case <-ctx.Done():
		}
		return func(bool) {}
	}
	if f.leaders == nil {
		f.leaders, f.warm = map[[32]byte]chan struct{}{}, map[[32]byte]time.Time{}
	}
	done := make(chan struct{})
	f.leaders[key] = done
	f.mu.Unlock()
	var once sync.Once
	return func(ok bool) {
		once.Do(func() {
			f.mu.Lock()
			delete(f.leaders, key)
			if ok {
				if len(f.warm) >= fanoutMax {
					for k, at := range f.warm {
						if time.Since(at) >= fanoutWarm {
							delete(f.warm, k)
						}
					}
					if len(f.warm) >= fanoutMax {
						f.warm = map[[32]byte]time.Time{} // ponytail: all live; an LRU if it ever matters
					}
				}
				f.warm[key] = time.Now()
			}
			f.mu.Unlock()
			close(done)
		})
	}
}

// fanoutKey keys a fresh child's shared prefix: its parent session, the
// upstream and model, and the request's tools and system prompt (Messages
// system, Responses instructions, chat's leading system message), which
// siblings of one agent type share, and its prompt_cache_key (OpenAI routes
// on it: siblings with different keys may not read each other's write). ok is false for any other request: not a
// child, not its first request (a forked child resends its parent's warm
// history), or nothing to share.
func fanoutKey(parent, upstream, model, grammar string, body []byte) (key [32]byte, ok bool) {
	wire := messagesWire // chat's messages read the same way here
	if grammar == translate.Responses {
		wire = responsesWire
	}
	root, okRoot := objectRoot(body)
	if parent == "" || !okRoot || !wire.fresh(body) {
		return key, false
	}
	hash := sha256.New()
	hash.Write([]byte(parent + "\x00" + upstream + "\x00" + model + "\x00"))
	shared := false
	for _, name := range []string{"tools", "system", "instructions", "prompt_cache_key"} {
		if span, found := jsonsplice.Field(body, root, name); found {
			hash.Write([]byte(name))
			hash.Write(body[span.Start:span.End])
			shared = shared || name != "prompt_cache_key"
		}
	}
	if grammar == translate.Chat {
		if _, _, items, ok := messageSpans(body); ok && len(items) > 0 {
			if role, _ := jsonsplice.StringField(body, items[0], "role"); role == "system" || role == "developer" {
				hash.Write(body[items[0].Start:items[0].End])
				shared = true
			}
		}
	}
	return [32]byte(hash.Sum(nil)), shared
}

// releaseOnRead releases a fan-out leader once its answer has content: a
// whole JSON answer at its first byte; an event stream at its first event
// that is not a comment, a ping or an opening event sent before the prompt is
// read (message_start, response.created, response.in_progress), and as not
// warm when that event is an error. The end of the body, or 64 KiB of
// openings, releases too.
type releaseOnRead struct {
	io.ReadCloser
	release func(bool)
	ok      bool // a 2xx answer
	stream  bool
	pending []byte // the stream's bytes since its last whole event
}

func (r *releaseOnRead) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	switch {
	case r.release == nil:
	case !r.stream:
		if n > 0 || err != nil {
			r.done(r.ok && n > 0)
		}
	default:
		r.pending = append(r.pending, bytes.ReplaceAll(p[:n], []byte("\r\n"), []byte("\n"))...)
		for r.release != nil {
			end := bytes.Index(r.pending, []byte("\n\n"))
			if end < 0 {
				break
			}
			event := r.pending[:end]
			r.pending = r.pending[end+2:]
			switch {
			case openingEventRE.Match(event) || !dataLineRE.Match(event):
			case errorEventRE.Match(event):
				r.done(false)
			default:
				r.done(r.ok)
			}
		}
		if r.release != nil && (err != nil || len(r.pending) > 64<<10) {
			r.done(false)
		}
	}
	return n, err
}

// sseEvents: an uncompressed text/event-stream, whose events the gate reads.
func sseEvents(header http.Header) bool {
	encoding := strings.TrimSpace(header.Get("Content-Encoding"))
	return strings.Contains(strings.ToLower(header.Get("Content-Type")), "text/event-stream") && (encoding == "" || strings.EqualFold(encoding, "identity"))
}

func (r *releaseOnRead) done(ok bool) {
	r.release(ok)
	r.release, r.pending = nil, nil
}

var (
	// dataLineRE: an event with a line that is not a comment (":" first).
	dataLineRE     = regexp.MustCompile(`(?m)^[^:\n]`)
	openingEventRE = regexp.MustCompile(`"type"\s*:\s*"(?:message_start|ping|response\.created|response\.in_progress)"`)
	errorEventRE   = regexp.MustCompile(`(?m)^event:\s*error|"type"\s*:\s*"(?:error|response\.failed)"`)
)
