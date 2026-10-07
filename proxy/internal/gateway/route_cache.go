package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"regexp"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/translate"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// Cache mechanics the route stage applies on its own: they keep a provider
// cache the session already paid for and decide nothing about models.
//   - prompt_cache_key: OpenAI routes a prefix to the machine holding it by
//     this key; a request without one gets the session's (hashed).
//   - OpenRouter: x-session-id (RouteTarget.Affinity) makes it sticky; once a
//     session's pool entry is warm on one of OpenRouter's providers it is
//     pinned there (provider.order + allow_fallbacks false), so an idle gap or
//     a provider error never spreads it to a cold one. A pinned request that
//     fails drops the pin: the next one is routed afresh.
//   - fan-out: sibling children starting on one new prefix go one first; the
//     rest wait for its first byte (or fanoutWait), then read its cache write.
//   - context tokens: the session's own bytes-to-tokens ratio, from the
//     provider's count, a child taking its parent's.

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
// one goes, the others wait until it has its first byte (the provider writes
// a prefix's cache entry before it answers), fanoutWait at most or until
// their own request is cancelled, then read that one write instead of each
// writing it again (cache.md rule 16: N parallel requests on one new prefix
// write N times). A prefix with a first byte in the last fanoutWarm is warm:
// nobody waits on it.
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
// followers go; ok marks the prefix warm (the leader got a first byte of a
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
// siblings of one agent type share. ok is false for any other request: not a
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
	for _, name := range []string{"tools", "system", "instructions"} {
		if span, found := jsonsplice.Field(body, root, name); found {
			hash.Write([]byte(name))
			hash.Write(body[span.Start:span.End])
			shared = true
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

// releaseOnRead releases a fan-out leader at its answer's first byte (or its
// end, or an error).
type releaseOnRead struct {
	io.ReadCloser
	release func(bool)
	ok      bool
}

func (r releaseOnRead) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 || err != nil {
		r.release(r.ok && n > 0)
	}
	return n, err
}
