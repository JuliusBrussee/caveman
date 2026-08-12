package gateway

import (
	"strings"
	"sync"
)

// prefixMonitor ports the agent SDK's providerFrozenExtends check
// (public/agent/src/runtime.ts) into the proxy: for each correlated session it
// remembers the previous request's ordered frozen-prefix component hashes and, on
// the next request, verifies the new prefix is an APPEND-ONLY EXTENSION of the
// prior one. A frozen prefix that reorders, mutates, or drops an already-frozen
// component (e.g. an in-band connector/system injection, issue #101) is not an
// extension and is flagged as a cache bust.
//
// It is OBSERVE-ONLY: it never blocks or modifies traffic. Its single output is a
// persisted cache_bust flag plus a Warn log naming the first diverging component
// index — the asymmetry the SDK could prove and the wrap could not.
type prefixMonitor struct {
	mu   sync.Mutex
	last map[string][]string
	// order tracks session insertion order for oldest-first eviction so a
	// long-running proxy's per-session state stays bounded (mirrors cacheguard).
	order []string
	cap   int
}

// defaultPrefixMonitorCap bounds retained sessions. An evicted session's next
// request is treated as a fresh first observation (no prior → no bust), which is
// the safe direction: eviction can only drop a warning, never fabricate one.
const defaultPrefixMonitorCap = 8192

func newPrefixMonitor() *prefixMonitor {
	return &prefixMonitor{last: map[string][]string{}, cap: defaultPrefixMonitorCap}
}

// observe compares this request's comma-joined frozen-prefix component hashes
// against the previous request in the same session, then records the current
// prefix as the new baseline. It reports whether the new prefix FAILED to extend
// the prior one and, when it did, the index of the first diverging component.
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
	prior, ok := m.last[sessionID]
	m.put(sessionID, current)
	if !ok {
		return false, -1
	}
	// Append-only extension: every prior component must reappear, in order, at the
	// same index. The first index where the current prefix is shorter than the
	// prior one or carries a different hash is the divergence.
	for i := range prior {
		if i >= len(current) || current[i] != prior[i] {
			return true, i
		}
	}
	return false, -1
}

// put records components for sessionID, tracking insertion order and evicting the
// oldest session once the cap is exceeded. Callers must hold m.mu.
func (m *prefixMonitor) put(sessionID string, components []string) {
	if _, exists := m.last[sessionID]; !exists {
		m.order = append(m.order, sessionID)
		for len(m.order) > m.cap {
			oldest := m.order[0]
			m.order = m.order[1:]
			delete(m.last, oldest)
		}
	}
	m.last[sessionID] = components
}
