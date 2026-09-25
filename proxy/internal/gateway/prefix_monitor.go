package gateway

import (
	"strings"
	"sync"
)

// prefixMonitor ports the agent SDK's providerFrozenExtends check
// (public/agent/src/runtime.ts) into the proxy: for each correlated session it
// remembers the ordered frozen-prefix component hashes of each conversation seen
// under it and, on the next request, verifies the new prefix is an APPEND-ONLY
// EXTENSION of the closest one. A frozen prefix that reorders, mutates, or drops
// an already-frozen message of the same conversation is not an extension and is
// flagged as a cache bust. A different system or tools component is read as a
// different conversation under the same session (see conversationComponents), so
// the in-band system injection of issue #101 is no longer flagged: without a
// conversation identity it is indistinguishable from a subagent's own prompt.
//
// It is OBSERVE-ONLY: it never blocks or modifies traffic. Its single output is a
// persisted cache_bust flag plus a Warn log naming the first diverging component
// index — the asymmetry the SDK could prove and the wrap could not.
type prefixMonitor struct {
	mu sync.Mutex
	// last holds, per correlated session, the frozen-prefix component hashes of
	// each conversation recently seen under it, least recently matched first. One
	// session is NOT one conversation: a header-less Claude Code process correlates
	// its main thread, every subagent and every side request to the same session
	// (unique_recent_session), and each carries its own system prompt. A single
	// anchor per session flip-flopped between them, so every request "diverged" at
	// the system component and compression was skipped session-wide (#1094).
	last map[string][][]string
	// order tracks session insertion order for oldest-first eviction so a
	// long-running proxy's per-session state stays bounded (mirrors cacheguard).
	order []string
	cap   int
	// observations counts calls that reached the comparison. observe RE-ANCHORS
	// the baseline, so a caller that runs it twice within one request anchors on
	// bytes the client never sent; the epoch gate's tests pin the call count.
	observations int
}

// defaultPrefixMonitorCap bounds retained sessions. An evicted session's next
// request is treated as a fresh first observation (no prior → no bust), which is
// the safe direction: eviction can only drop a warning, never fabricate one.
const defaultPrefixMonitorCap = 8192

// maxAnchorsPerSession bounds the conversations remembered per session; the least
// recently matched one is dropped first. Dropping can only turn a later drift into
// a fresh first observation, never fabricate a bust.
// ponytail: 16 anchors × 8192 sessions × up to 1024 hex hashes is gigabytes on a
// saturated hosted gateway; store fixed-width digests or lower the session cap
// if that deployment ever exists.
const maxAnchorsPerSession = 16

// conversationComponents is how many leading components identify a conversation
// rather than a turn of one: the framing literal, system, tools and the first
// frozen message (see anthropic.FrozenPrefixComponents). Two prefixes that already
// differ inside those are different conversations sharing a session — a subagent,
// a side request, a compaction — not a mutated prefix, and neither is a bust.
const conversationComponents = 4

func newPrefixMonitor() *prefixMonitor {
	return &prefixMonitor{last: map[string][][]string{}, cap: defaultPrefixMonitorCap}
}

// observe matches this request's comma-joined frozen-prefix component hashes
// against the conversations recently seen in the same session, taking the CLOSEST
// anchor (longest shared prefix; an anchor the request extends wins a tie). A
// prefix that APPEND-ONLY EXTENDS (or repeats) that anchor re-anchors it and is
// not a bust. Otherwise the prefix is anchored as a new conversation, and it is a
// bust only if it shares a whole conversation identity (conversationComponents)
// with the closest anchor and then diverges — a frozen message changed, dropped or
// reordered — reported as the index of the first diverging component. Closest, not
// first: a floor-0 request leaves a bare [literal, system, tools] anchor that every
// same-conversation request extends, and matching it would mask real drift.
//
// A missing session id or empty component list yields no comparison (bust=false,
// index=-1): the check needs a correlated session and provider-prefix evidence.
func (m *prefixMonitor) observe(sessionID, componentSHA256 string) (bust bool, divergingIndex int) {
	if m == nil || sessionID == "" || componentSHA256 == "" {
		return false, -1
	}
	current := strings.Split(componentSHA256, ",")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observations++
	anchors, ok := m.last[sessionID]
	if !ok {
		m.put(sessionID, [][]string{current})
		return false, -1
	}
	closest, shared, extends := -1, -1, false
	for i, prior := range anchors {
		n := commonPrefixLen(prior, current)
		ext := n == len(prior)
		if n > shared || (n == shared && ext && !extends) {
			closest, shared, extends = i, n, ext
		}
	}
	if extends {
		anchors = append(anchors[:closest], anchors[closest+1:]...)
		m.last[sessionID] = append(anchors, current)
		return false, -1
	}
	if len(anchors) >= maxAnchorsPerSession {
		anchors = anchors[1:]
	}
	m.last[sessionID] = append(anchors, current)
	if shared < conversationComponents {
		return false, -1
	}
	return true, shared
}

func commonPrefixLen(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// put records a session's anchors, tracking insertion order and evicting the
// oldest session once the cap is exceeded. Callers must hold m.mu.
func (m *prefixMonitor) put(sessionID string, anchors [][]string) {
	if _, exists := m.last[sessionID]; !exists {
		m.order = append(m.order, sessionID)
		for len(m.order) > m.cap {
			oldest := m.order[0]
			m.order = m.order[1:]
			delete(m.last, oldest)
		}
	}
	m.last[sessionID] = anchors
}
