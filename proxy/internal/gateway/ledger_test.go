package gateway

import (
	"strconv"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// The session ledger is the accounting the harm tripwire reads, and the freeze
// registry every lever consults. Its contract: bounded, session-scoped,
// one-way, and completely inert for a caller that sends no session id.

func creation(tokens int) providers.UsageObservation {
	return providers.UsageObservation{InputTokens: tokens, CacheCreationInputTokens: tokens}
}

// warm gives a session a baseline the tripwire can measure against, with
// the lever recorded as active throughout.
func warm(l *sessionLedger, session string, lv lever, calls, tokens int) {
	for i := 0; i < calls; i++ {
		l.Observe(session, []lever{lv}, creation(tokens))
	}
}

func TestLedgerIsInertWithoutASessionID(t *testing.T) {
	l := newSessionLedger()
	for i := 0; i < tripwireStrikesToFreeze+2; i++ {
		if froze := l.Observe("", []lever{leverToolSchemaStrip}, creation(10_000_000)); froze != nil {
			t.Fatalf("an unidentified session was accounted for: %v", froze)
		}
	}
	if !l.LeverAllowed("", leverToolSchemaStrip) {
		t.Fatal("an unidentified session lost a lever")
	}
	if l.FrozenLevers("") != nil {
		t.Fatal("an unidentified session reported frozen levers")
	}
	if len(l.entries) != 0 {
		t.Fatalf("the ledger created %d entries for sessionless traffic", len(l.entries))
	}
}

// TestLedgerFreezesAfterThreeStrikes walks the whole tripwire: a quiet baseline,
// three anomalous calls, the freeze, and the fail-open consequence.
func TestLedgerFreezesAfterThreeStrikes(t *testing.T) {
	l := newSessionLedger()
	warm(l, "s1", leverToolSchemaStrip, 4, 1_000)

	spike := creation(10 * tripwireCacheCreationFloorTokens)
	for i := 1; i < tripwireStrikesToFreeze; i++ {
		if froze := l.Observe("s1", []lever{leverToolSchemaStrip}, spike); len(froze) != 0 {
			t.Fatalf("froze on strike %d, want %d strikes", i, tripwireStrikesToFreeze)
		}
		if !l.LeverAllowed("s1", leverToolSchemaStrip) {
			t.Fatalf("lever taken away on strike %d", i)
		}
	}
	froze := l.Observe("s1", []lever{leverToolSchemaStrip}, spike)
	if len(froze) != 1 || froze[0] != leverToolSchemaStrip {
		t.Fatalf("third strike froze %v, want [%s]", froze, leverToolSchemaStrip)
	}
	if l.LeverAllowed("s1", leverToolSchemaStrip) {
		t.Fatal("a frozen lever is still allowed")
	}
	// Only the lever that was active is taken; the other keeps running.
	if !l.LeverAllowed("s1", leverBreakpointPlan) {
		t.Fatal("an inactive lever was frozen by another lever's strikes")
	}
	if got := l.FrozenLevers("s1"); len(got) != 1 || got[0] != leverToolSchemaStrip {
		t.Fatalf("FrozenLevers = %v", got)
	}
	// The freeze is one-way. A long quiet stretch must not thaw it: thawing
	// reintroduces exactly the oscillation the freeze exists to stop.
	for i := 0; i < 50; i++ {
		l.Observe("s1", nil, creation(10))
	}
	if l.LeverAllowed("s1", leverToolSchemaStrip) {
		t.Fatal("a quiet stretch un-froze a lever")
	}
	// Freezing is scoped to one session; a new agent run starts clean.
	if !l.LeverAllowed("s2", leverToolSchemaStrip) {
		t.Fatal("freezing one session took the lever from another")
	}
}

// TestLedgerRequiresBothThresholds pins that neither half of the rule fires
// alone: a huge ratio under the floor is noise, and a huge absolute number that
// matches the session's own normal is just how this session bills.
func TestLedgerRequiresBothThresholds(t *testing.T) {
	t.Run("under the absolute floor", func(t *testing.T) {
		l := newSessionLedger()
		warm(l, "s", leverBreakpointPlan, 4, 10)
		for i := 0; i < tripwireStrikesToFreeze+1; i++ {
			l.Observe("s", []lever{leverBreakpointPlan}, creation(tripwireCacheCreationFloorTokens))
		}
		if !l.LeverAllowed("s", leverBreakpointPlan) {
			t.Fatal("a spike at the floor tripped the wire")
		}
	})
	t.Run("large but within the session's own normal", func(t *testing.T) {
		l := newSessionLedger()
		warm(l, "s", leverBreakpointPlan, 4, 400_000)
		for i := 0; i < tripwireStrikesToFreeze+1; i++ {
			l.Observe("s", []lever{leverBreakpointPlan}, creation(800_000))
		}
		if !l.LeverAllowed("s", leverBreakpointPlan) {
			t.Fatal("a call inside 3x the baseline mean tripped the wire")
		}
	})
}

// TestLedgerJudgesThePreviousRequestsLevers: a lever is judged by what the NEXT
// request had to pay to re-cache the prefix it produced. A spike on a request
// whose predecessor ran no lever belongs to the traffic, not to us.
func TestLedgerJudgesThePreviousRequestsLevers(t *testing.T) {
	l := newSessionLedger()
	warm(l, "s", leverToolSchemaStrip, 4, 1_000)
	// Every spike is preceded by a request with no lever active.
	spike := creation(10 * tripwireCacheCreationFloorTokens)
	for i := 0; i < tripwireStrikesToFreeze+2; i++ {
		l.Observe("s", nil, creation(1_000))
		l.Observe("s", nil, spike)
	}
	if !l.LeverAllowed("s", leverToolSchemaStrip) {
		t.Fatal("a lever was blamed for spikes it did not precede")
	}
	// The first call of a session can never strike: there is no baseline yet.
	fresh := newSessionLedger()
	if froze := fresh.Observe("new", []lever{leverToolSchemaStrip}, spike); len(froze) != 0 {
		t.Fatalf("the first call of a session struck: %v", froze)
	}
}

// TestLedgerEvictsLeastRecentlyUsed: the ledger is bounded, so a client that
// mints a fresh session per request cannot grow it without limit — and the
// session still in use survives the churn.
func TestLedgerEvictsLeastRecentlyUsed(t *testing.T) {
	l := newSessionLedger()
	l.Observe("keeper", []lever{leverBreakpointPlan}, creation(1_000))

	for i := 0; i < ledgerMaxSessions; i++ {
		id := "churn-" + strconv.Itoa(i)
		l.Observe(id, nil, creation(1))
		// Touching the keeper keeps it at the front of the LRU order.
		l.LeverAllowed("keeper", leverBreakpointPlan)
	}

	if len(l.entries) != ledgerMaxSessions || l.order.Len() != ledgerMaxSessions {
		t.Fatalf("ledger holds %d entries (order %d), want the %d cap",
			len(l.entries), l.order.Len(), ledgerMaxSessions)
	}
	if _, ok := l.entries["keeper"]; !ok {
		t.Fatal("the recently-used session was evicted while cold sessions survived")
	}
	if _, ok := l.entries["churn-0"]; ok {
		t.Fatal("the least-recently-used session was not evicted")
	}
}

// TestLedgerEvictionForgetsAFreeze is the honest consequence of a bounded
// ledger: eviction is a loss of memory, so an evicted session's lever comes back.
// It is the fail-open direction, and it is bounded by the cap being far larger
// than any real operator's concurrent session count.
func TestLedgerEvictionForgetsAFreeze(t *testing.T) {
	l := newSessionLedger()
	warm(l, "victim", leverBreakpointPlan, 4, 1_000)
	spike := creation(10 * tripwireCacheCreationFloorTokens)
	for i := 0; i < tripwireStrikesToFreeze; i++ {
		l.Observe("victim", []lever{leverBreakpointPlan}, spike)
	}
	if l.LeverAllowed("victim", leverBreakpointPlan) {
		t.Fatal("setup failed: the lever was not frozen")
	}
	for i := 0; i < ledgerMaxSessions+1; i++ {
		l.Observe("churn-"+strconv.Itoa(i), nil, creation(1))
	}
	if _, ok := l.entries["victim"]; ok {
		t.Fatal("setup failed: the frozen session was not evicted")
	}
	if !l.LeverAllowed("victim", leverBreakpointPlan) {
		t.Fatal("an evicted session must be indistinguishable from an unknown one")
	}
}

func TestLedgerTracksSessionTotals(t *testing.T) {
	l := newSessionLedger()
	l.Observe("s", nil, providers.UsageObservation{InputTokens: 100, CachedInputTokens: 60, CacheCreationInputTokens: 10})
	l.Observe("s", nil, providers.UsageObservation{InputTokens: 200, CachedInputTokens: 150, CacheCreationInputTokens: 20})

	entry := l.entries["s"].Value.(*ledgerEntry)
	if entry.calls != 2 || entry.cumulativeInput != 300 ||
		entry.cumulativeCacheRead != 210 || entry.cumulativeCacheCreation != 30 {
		t.Fatalf("cumulative totals wrong: %+v", entry)
	}
	if entry.lastInputTokens != 200 || entry.lastCacheReadTokens != 150 || entry.lastCacheCreationTokens != 20 {
		t.Fatalf("last-call values wrong: %+v", entry)
	}
}

// TestLedgerNilReceiverIsPermissive: a Server built without a ledger must not
// take a lever away or panic. The tripwire only ever subtracts capability.
func TestLedgerNilReceiverIsPermissive(t *testing.T) {
	var l *sessionLedger
	if !l.LeverAllowed("s", leverBreakpointPlan) {
		t.Fatal("a nil ledger denied a lever")
	}
	if l.FrozenLevers("s") != nil || l.Observe("s", []lever{leverBreakpointPlan}, creation(1)) != nil {
		t.Fatal("a nil ledger reported state")
	}
}
