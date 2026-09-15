package gateway

import (
	"container/list"
	"sync"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// lever names one optimization the session ledger can account for and freeze.
// A lever is anything that rewrites bytes (or provider-native metadata) in a way
// that could move the provider cache prefix — the class of change whose harm
// shows up as cache-creation growth rather than as an error.
type lever string

const (
	// leverToolSchemaStrip is the tool-schema annotation strip (toolschema_strip.go).
	leverToolSchemaStrip lever = toolSchemaStripOptimizerID
	// leverBreakpointPlan is the cache-breakpoint planner (breakpoint_plan.go).
	leverBreakpointPlan lever = breakpointPlanOptimizerID
)

// ledgerMaxSessions bounds the ledger. A wrap session is a long-lived agent run,
// so a few thousand entries is far more than one operator produces; the cap
// exists so a client that mints a fresh x-cave-session per request cannot grow
// the map without bound. Eviction is least-recently-used.
const ledgerMaxSessions = 1024

// The tripwire thresholds. A lever trips when the request AFTER it ran paid a
// cache-creation bill that is both large in absolute terms and far above what
// this session normally pays — the signature of a rewrite that failed to
// stabilize into a reusable prefix (spec §6: Δturns / Δcache-reads rising while
// quality holds).
//
// These three numbers are deliberately conservative rather than measured: they
// are set so that only an unmistakable regression trips them, which means the
// tripwire under-fires rather than disabling a lever that was working.
// Follow-up: replace them with values derived from the replay grid's repo-scale
// corpus, where the normal cache-creation distribution per agent is known.
const (
	// tripwireCacheCreationFloorTokens is the absolute floor. Below it, a spike is
	// not worth acting on no matter how large the ratio (a session whose baseline
	// mean is 50 tokens hits 3x constantly and means nothing).
	tripwireCacheCreationFloorTokens = 50_000
	// tripwireCacheCreationMultiple is how far above the session's own baseline
	// mean cache-creation a call must land to count as anomalous.
	tripwireCacheCreationMultiple = 3
	// tripwireStrikesToFreeze is how many anomalous calls freeze the lever. One
	// spike is a cold cache or a genuinely new prefix; three is a pattern.
	tripwireStrikesToFreeze = 3
)

// sessionLedger is the per-session accounting the harm tripwire reasons over.
//
// It is keyed by the caller-supplied x-cave-session value and holds nothing but
// counters: no bodies, no headers, no content. A request with no session header
// gets no entry at all, and every method is then inert — a caller that does not
// identify its session simply never trips and never freezes.
//
// Freezing is one-way within a session. A frozen lever stays frozen until the
// session id changes (a new agent run), because the alternative — thawing on a
// quiet stretch — reintroduces exactly the oscillation the freeze exists to stop.
type sessionLedger struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	// order is most-recently-used at the front; the back is the eviction target.
	order *list.List
}

// ledgerEntry is one session's running account. cumulative* fields include every
// observed call; last* fields are the most recent call's values.
type ledgerEntry struct {
	sessionID string

	calls                   int
	cumulativeInput         int64
	cumulativeCacheRead     int64
	cumulativeCacheCreation int64
	lastInputTokens         int
	lastCacheReadTokens     int
	lastCacheCreationTokens int
	// baselineCalls/baselineCacheCreation are the session's NORMAL cache-creation,
	// which is what the tripwire compares against — spec §6's "the wrap's own
	// baseline window". Calls the tripwire already judged anomalous are excluded
	// from it. A plain running mean over every call would fold each spike into the
	// very number the next spike is measured against, so a persistent regression
	// would raise its own bar out of reach after one or two calls and never reach
	// a freeze — the detector would be blind to exactly the failure it exists for.
	baselineCalls         int
	baselineCacheCreation int64
	strikes               map[lever]int
	frozen                map[lever]bool
	// activePrev is the set of levers that ran on the PREVIOUS request of this
	// session. The tripwire reads it rather than the current request's set: a
	// lever that rewrote turn N is judged by what turn N+1 had to pay to re-cache
	// the prefix it produced.
	activePrev map[lever]bool
}

func newSessionLedger() *sessionLedger {
	return &sessionLedger{entries: map[string]*list.Element{}, order: list.New()}
}

// LeverAllowed reports whether a lever may run for this session. An empty session
// id, a nil ledger, and an unknown session all allow it — the tripwire only ever
// takes capability away, and only from a session it has watched trip.
func (l *sessionLedger) LeverAllowed(sessionID string, lv lever) bool {
	if l == nil || sessionID == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	element, ok := l.entries[sessionID]
	if !ok {
		return true
	}
	l.order.MoveToFront(element)
	return !element.Value.(*ledgerEntry).frozen[lv]
}

// FrozenLevers returns this session's frozen levers in a stable order, for the
// x-caveman-tripwire disclosure header.
func (l *sessionLedger) FrozenLevers(sessionID string) []lever {
	if l == nil || sessionID == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	element, ok := l.entries[sessionID]
	if !ok {
		return nil
	}
	entry := element.Value.(*ledgerEntry)
	var out []lever
	// Iterate the declared lever order, not the map, so the header is stable.
	for _, lv := range []lever{leverToolSchemaStrip, leverBreakpointPlan} {
		if entry.frozen[lv] {
			out = append(out, lv)
		}
	}
	return out
}

// Observe records one completed call against a session and evaluates the harm
// tripwire. active is the set of levers that ran on THIS request; the returned
// slice is the levers this call newly froze (empty in the normal case), so the
// caller can log the transition exactly once.
//
// The tripwire fires when a lever was active on the previous request AND this
// call's cache-creation exceeds BOTH the absolute floor AND the multiple of the
// session's baseline mean. The baseline is built from the calls before this one,
// so the very first call of a session can never strike.
func (l *sessionLedger) Observe(sessionID string, active []lever, usage providers.UsageObservation) []lever {
	if l == nil || sessionID == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	entry := l.entryLocked(sessionID)

	creation := usage.CacheCreationInputTokens
	anomalous := false
	if entry.baselineCalls > 0 {
		baselineMean := float64(entry.baselineCacheCreation) / float64(entry.baselineCalls)
		anomalous = creation > tripwireCacheCreationFloorTokens &&
			float64(creation) > tripwireCacheCreationMultiple*baselineMean
	}

	var froze []lever
	if anomalous {
		for _, lv := range []lever{leverToolSchemaStrip, leverBreakpointPlan} {
			if !entry.activePrev[lv] || entry.frozen[lv] {
				continue
			}
			entry.strikes[lv]++
			if entry.strikes[lv] >= tripwireStrikesToFreeze {
				entry.frozen[lv] = true
				froze = append(froze, lv)
			}
		}
	} else {
		entry.baselineCalls++
		entry.baselineCacheCreation += int64(creation)
	}

	entry.calls++
	entry.cumulativeInput += int64(usage.InputTokens)
	entry.cumulativeCacheRead += int64(usage.CachedInputTokens)
	entry.cumulativeCacheCreation += int64(creation)
	entry.lastInputTokens = usage.InputTokens
	entry.lastCacheReadTokens = usage.CachedInputTokens
	entry.lastCacheCreationTokens = creation

	// activePrev is a single slot, so concurrent requests on ONE session are
	// last-writer-wins: two calls in flight together can overwrite each other's
	// lever set before the next call reads it. The consequence is bounded and
	// points the safe way. A lost write can only DROP a lever from the set the
	// next call is judged against, which loses a strike (under-fires); a stale
	// write can only carry a lever the immediately preceding call also ran, which
	// at worst mis-attributes one strike inside a three-strike budget. Neither can
	// freeze a lever that was never active in the session, and neither can
	// un-freeze one. Ordering a session's calls to fix this would mean serializing
	// them, which is a far worse trade than an occasionally missed strike.
	entry.activePrev = map[lever]bool{}
	for _, lv := range active {
		entry.activePrev[lv] = true
	}
	return froze
}

// entryLocked returns the session's entry, creating it (and evicting the
// least-recently-used session when the ledger is full) as needed.
func (l *sessionLedger) entryLocked(sessionID string) *ledgerEntry {
	if element, ok := l.entries[sessionID]; ok {
		l.order.MoveToFront(element)
		return element.Value.(*ledgerEntry)
	}
	for l.order.Len() >= ledgerMaxSessions {
		oldest := l.order.Back()
		if oldest == nil {
			break
		}
		l.order.Remove(oldest)
		delete(l.entries, oldest.Value.(*ledgerEntry).sessionID)
	}
	entry := &ledgerEntry{
		sessionID:  sessionID,
		strikes:    map[lever]int{},
		frozen:     map[lever]bool{},
		activePrev: map[lever]bool{},
	}
	l.entries[sessionID] = l.order.PushFront(entry)
	return entry
}
