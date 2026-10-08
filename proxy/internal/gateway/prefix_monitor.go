package gateway

import (
	"bytes"
	"crypto/sha256"
	"slices"
	"sync"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// prefixMonitor is the runtime tripwire for the cache invariant that
// cache_prefix_invariant_test.go proves: when a request's client bytes repeat
// a prefix an earlier accepted request of the session cached, the bytes
// forwarded over that prefix must repeat what was forwarded then. Each anchor
// keeps both views of an accepted request's cached prefix — what the client
// sent and what went upstream — and a request that does not extend what the
// session cached is classified by who changed the bytes:
//
//   - client: the client's own bytes changed (an edit, a rewind, a compaction).
//     Flagged on the row and logged at DEBUG; nearly every old warning was one.
//   - caveman: the client's bytes are unchanged and the forwarded bytes are not,
//     or the forwarded request caches less than the client asked for (a
//     dropped breakpoint). That is a caveman bug: logged at ERROR, counted by
//     stats and status.
//   - stream_switch: a PAYG request without MCP recovery streamed after its
//     conversation was compressed. The server-side retrieve tool cannot ride a
//     stream, so it goes out raw and the conversation stays raw from there
//     (raw_pin.go): the invariant's second exception, logged at WARN.
//   - lever_freeze: the session's harm tripwire froze the tool-schema strip, and
//     the request after it goes out with the original catalog: a deliberate,
//     one-time rollover (see stripToolSchema), logged at WARN.
//   - raw_retry: the provider rejected the transformed request and accepted the
//     original bytes, the invariant's first exception. Requests extending it are
//     held to the raw request: the raw pin that records it (raw_pin.go) exempts
//     the anchors it covers, whether or not the retry was observed here. A
//     request that follows a longer replaced prefix than the pin covers is held
//     to that prefix instead, and not to the raw anchor (the longest lineage
//     wins, as in raw_pin.go).
//
// It is OBSERVE-ONLY: it never blocks or modifies traffic. A session is not one
// conversation — a header-less Claude Code process correlates its main thread,
// subagents and side requests to one session (#1094) — so it keeps several
// anchors per session and compares a request only with those it extends.
type prefixMonitor struct {
	mu sync.Mutex
	// last holds the anchors of each session, least recently written first.
	last map[string][]prefixAnchor
	// order tracks session insertion order for oldest-first eviction so a
	// long-running proxy's per-session state stays bounded (mirrors cacheguard).
	order []string
	cap   int
}

// prefixAnchor is one accepted request's cached prefix as component digests.
// raw marks a request that went out raw because of a raw retry or a pin, and
// seq orders its acceptance against later sends (Server.prefixSeq).
type prefixAnchor struct {
	client, forwarded [][32]byte
	raw               bool
	seq               uint64
}

const (
	bustCauseClient       = "client"
	bustCauseCaveman      = "caveman"
	bustCauseRawRetry     = "raw_retry"
	bustCauseLeverFreeze  = "lever_freeze"
	bustCauseStreamSwitch = "stream_switch"
)

// defaultPrefixMonitorCap bounds retained sessions. An evicted session's next
// request is treated as a fresh first observation (no prior → no bust), which is
// the safe direction: eviction can only drop a warning, never fabricate one.
const defaultPrefixMonitorCap = 8192

// maxAnchorsPerSession bounds the anchors remembered per session; the least
// recently written one is dropped first. Dropping can only turn a later drift
// into a fresh first observation, never fabricate a bust.
// ponytail: 16 anchors × 8192 sessions × up to 1024 digest pairs is gigabytes
// on a saturated hosted gateway; lower the session cap if that deployment ever
// exists.
const maxAnchorsPerSession = 16

// conversationComponents is how many leading components identify a conversation
// rather than a turn of one: system, tools and the first message (see
// anthropic.CachedPrefixComponents). A request that already differs inside those
// is another conversation sharing the session — a subagent, a side request — so
// it is never a bust.
const conversationComponents = 3

func newPrefixMonitor() *prefixMonitor {
	return &prefixMonitor{last: map[string][]prefixAnchor{}, cap: defaultPrefixMonitorCap}
}

// observation is one accepted request as the tripwire sees it.
type observation struct {
	// client and forwarded are its cached-prefix components as the client sent
	// them and as they went upstream (see cachedPrefix); cached is how many of
	// them it caches.
	client, forwarded [][]byte
	cached            int
	// rebase names the exception it is, if any: bustCauseRawRetry (the provider
	// accepted only the client's original bytes) or bustCauseStreamSwitch. Such
	// a request may differ from what it extends, and later ones follow it.
	rebase string
	// pinned is how many leading components a raw pin it extends covers, when
	// it went out raw. followed is the longest prefix of it that went out
	// replaced past its longest raw anchor: such a request follows that lineage,
	// not the shorter raw one (raw_pin.go).
	pinned, followed int
	// rollover is how many leading components a deliberate rollover may have
	// changed: the agent-wide ones when the harm tripwire froze the
	// tool-schema strip this request would have taken, 0 otherwise.
	rollover int
	// sent and accepted order the request: only anchors accepted before its
	// forwarding was decided bind it, since one still in flight then may not
	// have been cached yet.
	sent, accepted uint64
}

// observe checks one accepted request against the anchors of its session and
// records it as an anchor.
//
// Every anchor whose cached client prefix the request repeats must find its
// forwarded prefix repeated too; the first component that is not is a caveman
// bust (raw_retry for the retry itself), unless a raw pin covers the anchor —
// the provider re-cached those bytes raw — or the anchor went out raw and the
// request follows a longer replaced lineage. Otherwise, a request whose closest
// anchor shares the conversation identity but is not repeated is a client bust
// at the first differing component. "" and -1 mean the request extends what was
// cached, or there was nothing to compare: no session, or no comparable
// components.
func (m *prefixMonitor) observe(session string, o observation) (cause string, index int) {
	client, forwarded, cached, rebase, pinned := o.client, o.forwarded, o.cached, o.rebase != "", o.pinned
	if m == nil || session == "" || len(client) == 0 || len(forwarded) != len(client) || cached < 0 || cached > len(client) {
		return "", -1
	}
	cur := prefixAnchor{client: digestEach(client), forwarded: digestEach(forwarded), raw: rebase || pinned > 0, seq: o.accepted}
	m.mu.Lock()
	defer m.mu.Unlock()
	anchors := m.last[session]
	cause, index = "", -1
	closest, closestRepeated := -1, false
	var kept []prefixAnchor
	for _, a := range anchors {
		if a.seq > o.sent && o.sent > 0 {
			kept = append(kept, a) // accepted while this request was in flight
			continue
		}
		n := commonPrefixLen(a.client, cur.client)
		repeats := n == len(a.client)
		if n > closest || (n == closest && repeats && !closestRepeated) {
			closest, closestRepeated = n, repeats
		}
		if !repeats {
			kept = append(kept, a)
			continue
		}
		preserved := commonPrefixLen(a.forwarded, cur.forwarded)
		diverged := preserved < len(a.forwarded) && (rebase || pinned < len(a.forwarded)) && !(a.raw && o.followed > len(a.client))
		if diverged && (index < 0 || preserved < index) {
			cause, index = bustCauseCaveman, preserved
			switch {
			case rebase:
				cause = o.rebase
			case preserved < o.rollover:
				cause = bustCauseLeverFreeze
			}
		}
		// The request takes over an anchor it repeats when it caches at least as
		// much — it is the provider's latest entry for those bytes — and always
		// when it is an exception, whose raw bytes the provider now holds there.
		if rebase || cached >= len(a.client) {
			continue
		}
		kept = append(kept, a)
	}
	if cause == "" && !closestRepeated && closest >= conversationComponents {
		cause, index = bustCauseClient, closest
	}
	if cached > 0 {
		cur.client, cur.forwarded = cur.client[:cached], cur.forwarded[:cached]
		kept = append(kept, cur)
	}
	if len(kept) > maxAnchorsPerSession {
		kept = kept[len(kept)-maxAnchorsPerSession:]
	}
	m.put(session, kept)
	return cause, index
}

// longestRawAnchor returns the length of the longest anchor of session that
// went out raw and that client repeats; 0 when there is none.
func (m *prefixMonitor) longestRawAnchor(session string, client [][]byte) int {
	if m == nil || session == "" {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	var digests [][32]byte
	for _, a := range m.last[session] {
		if !a.raw || len(a.client) <= n || len(a.client) > len(client) {
			continue
		}
		if digests == nil {
			digests = digestEach(client)
		}
		if commonPrefixLen(a.client, digests) == len(a.client) {
			n = len(a.client)
		}
	}
	return n
}

// acceptance is what the request path knows about an accepted request that
// the tripwire needs.
type acceptance struct {
	// rawRetry: the provider accepted only the client's original bytes.
	// streamRaw: a PAYG stream the server-side retrieve path sent raw.
	rawRetry, streamRaw bool
	// stripFrozen: the harm tripwire froze a tool-schema strip that would
	// otherwise have run on this request.
	stripFrozen bool
	// sent is the Server.prefixSeq value taken before the request's forwarding
	// was decided.
	sent               uint64
	session, requestID string
}

// observeCachedPrefix runs once per accepted request — body as the client sent
// it, accepted as it went upstream. It records the request's lineage when its
// cached prefix went out replaced (raw_pin.go), then runs the tripwire, logs
// what it found and returns the cause. A provider caches per model, so the
// model is part of the tripwire's key.
func (s *Server) observeCachedPrefix(adapter providers.Adapter, meta providers.RequestMetadata, body, accepted []byte, a acceptance) string {
	sessionID, requestID := a.session, a.requestID
	sent := bytes.Equal(accepted, body)
	if sent && sessionID == "" && !a.streamRaw {
		return "" // raw bytes record no lineage, and the tripwire needs a session
	}
	client, cached, ok := cachedPrefix(adapter, meta, body)
	if !ok {
		return ""
	}
	key := sessionID + "\x00" + meta.Model
	o := observation{client: client, forwarded: client, cached: cached, sent: a.sent, accepted: s.prefixSeq.Add(1)}
	switch {
	case a.rawRetry:
		o.rebase = bustCauseRawRetry
	case a.streamRaw:
		o.rebase = bustCauseStreamSwitch
		s.recordLineage(adapter, client, cached, lineageStreamRaw)
	}
	if a.stripFrozen {
		o.rollover = sharedComponents(adapter)
	}
	forwardedCached := cached
	if sent {
		// Only a request that went out as sent can be one held to a raw pin.
		o.pinned = s.rawPinCoverage(adapter, newPrefixDigests(client))
	} else if o.forwarded, forwardedCached, ok = cachedPrefix(adapter, meta, accepted); !ok || len(o.forwarded) != len(client) {
		return ""
	} else if !componentsEqual(o.forwarded[:cached], client[:cached]) {
		// Did it follow a replaced lineage past a raw anchor it repeats? Asked
		// before this request records its own lineage.
		if raw := s.prefixMonitor.longestRawAnchor(key, client); raw > 0 && s.prefixCache != nil {
			if n, form := s.longestLineage(newPrefixDigests(client), raw); form == lineageReplaced {
				o.followed = n
			}
		}
		s.recordLineage(adapter, client, cached, lineageReplaced)
	}
	if sessionID == "" {
		return ""
	}
	if o.rebase != "" && cached <= sharedComponents(adapter) {
		// An exception that cached only what every conversation of the agent
		// shares re-bases nothing (raw_pin.go), so it leaves no anchor to hold
		// later requests to.
		o.cached = 0
	}
	cause, index := s.prefixMonitor.observe(key, o)
	attrs := []any{"request_id", requestID, "session_id", sessionID, "index", index}
	if forwardedCached < cached {
		// The provider writes no entry at a breakpoint caveman removed, so the
		// next turn has none to read, whatever the anchors say.
		if s.logger != nil {
			s.logger.Error("caveman dropped a cache breakpoint the client set", append(attrs, "cached", cached, "forwarded_cached", forwardedCached)...)
		}
		return bustCauseCaveman
	}
	if s.logger != nil {
		switch cause {
		case bustCauseCaveman:
			s.logger.Error("caveman changed bytes the provider already cached", attrs...)
		case bustCauseRawRetry:
			s.logger.Warn("provider accepted only the original bytes; its cached prefix restarts here", attrs...)
		case bustCauseLeverFreeze:
			s.logger.Warn("the harm tripwire froze the tool-schema strip; the cached prefix restarts here", attrs...)
		case bustCauseStreamSwitch:
			s.logger.Warn("a compressed PAYG conversation streamed; the server-side retrieve tool cannot ride a stream, so its cached prefix restarts raw here", attrs...)
		case bustCauseClient:
			s.logger.Debug("client changed bytes the provider already cached", attrs...)
		}
	}
	return cause
}

func componentsEqual(a, b [][]byte) bool {
	return slices.EqualFunc(a, b, bytes.Equal)
}

func digestEach(components [][]byte) [][32]byte {
	out := make([][32]byte, len(components))
	for i, c := range components {
		out[i] = sha256.Sum256(c)
	}
	return out
}

func commonPrefixLen[T comparable](a, b []T) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// put records a session's anchors, tracking insertion order and evicting the
// oldest session once the cap is exceeded. Callers must hold m.mu.
func (m *prefixMonitor) put(session string, anchors []prefixAnchor) {
	if _, exists := m.last[session]; !exists {
		m.order = append(m.order, session)
		for len(m.order) > m.cap {
			oldest := m.order[0]
			m.order = m.order[1:]
			delete(m.last, oldest)
		}
	}
	m.last[session] = anchors
}
