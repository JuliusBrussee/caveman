package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// storeEngine is the slice of Engine the retrieve path needs, backed by a map of
// stored originals so the anti-storm rules can be exercised without a real store.
type storeEngine struct {
	originals map[string]string
	fullCalls int
	narrowed  int
}

func (e *storeEngine) Compress([]byte, engine.Options) (engine.Result, error) {
	return engine.Result{}, nil
}
func (e *storeEngine) Retrieve(handle string) ([]byte, error) {
	original, ok := e.originals[handle]
	if !ok {
		return nil, fmt.Errorf("unknown handle")
	}
	return []byte(original), nil
}
func (e *storeEngine) RetrieveQuery(handle, query string) ([]byte, error) {
	original, err := e.Retrieve(handle)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		e.fullCalls++
		return original, nil
	}
	e.narrowed++
	// Stand in for BM25 narrowing: return only the matching lines' records.
	var kept []string
	for _, line := range strings.Split(string(original), "\n") {
		if strings.Contains(line, query) {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return original, nil
	}
	return []byte(strings.Join(kept, "\n")), nil
}
func (e *storeEngine) Stats() (ccr.Stats, error)            { return ccr.Stats{}, nil }
func (e *storeEngine) EncodeTOON(in []byte) ([]byte, error) { return in, nil }
func (e *storeEngine) DecodeTOON(in []byte) ([]byte, error) { return in, nil }

func newStoreEngine() *storeEngine {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "dlv-%04d status=delivered endpoint=alpha\n", 2000+i)
	}
	return &storeEngine{originals: map[string]string{"ccr_page1": b.String(), "ccr_page2": "other content\nsecond line\n"}}
}

func retrieveArgs(handle, query string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"recovery_handle": handle, "query": query})
	return raw
}

func resultText(t *testing.T, r ToolResult) string {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestRepeatedRetrieveIsAnsweredWithAPointer covers rule 1: an identical
// (handle, query) is never re-sent, because the bytes are verbatim above.
func TestRepeatedRetrieveIsAnsweredWithAPointer(t *testing.T) {
	eng := newStoreEngine()
	session := newRetrieveSession()

	first := resultText(t, retrieveTool(eng, session, retrieveArgs("ccr_page1", "dlv-2007")))
	if !strings.Contains(first, "dlv-2007") {
		t.Fatalf("the first retrieve must return content: %s", first)
	}
	second := resultText(t, retrieveTool(eng, session, retrieveArgs("ccr_page1", "dlv-2007")))
	if !strings.Contains(second, "already answered earlier in this session") {
		t.Fatalf("the repeat must return the pointer note: %s", second)
	}
	if strings.Contains(second, "endpoint=alpha") {
		t.Fatalf("the repeat must not re-send the content: %s", second)
	}
	// Whitespace-only differences are the same request, not a new one.
	padded := resultText(t, retrieveTool(eng, session, retrieveArgs("ccr_page1", "  dlv-2007  ")))
	if !strings.Contains(padded, "already answered earlier in this session") {
		t.Fatalf("a whitespace-padded repeat is still a repeat: %s", padded)
	}
	// A DIFFERENT query on the same handle is new content and must be served.
	fresh := resultText(t, retrieveTool(eng, session, retrieveArgs("ccr_page1", "dlv-2011")))
	if !strings.Contains(fresh, "dlv-2011") {
		t.Fatalf("a new query must still be answered: %s", fresh)
	}
}

// TestRetrieveStormPaysOutInFull covers rule 2: past the threshold the next
// retrieve returns the whole stored original and says so.
func TestRetrieveStormPaysOutInFull(t *testing.T) {
	eng := newStoreEngine()
	session := newRetrieveSession()

	for i := 0; i < retrieveStormThreshold; i++ {
		out := resultText(t, retrieveTool(eng, session, retrieveArgs("ccr_page1", fmt.Sprintf("dlv-%04d", 2000+i))))
		if strings.Contains(out, "COMPLETE stored original") {
			t.Fatalf("payout came early, at retrieve %d: %s", i+1, out)
		}
	}
	if eng.fullCalls != 0 {
		t.Fatalf("no full recovery should have happened yet, got %d", eng.fullCalls)
	}

	storm := resultText(t, retrieveTool(eng, session, retrieveArgs("ccr_page1", "dlv-2030")))
	if !strings.Contains(storm, "COMPLETE stored original") {
		t.Fatalf("the retrieve past the threshold must pay out in full: %s", storm)
	}
	if eng.fullCalls != 1 {
		t.Fatalf("the payout must be an unfiltered recovery, got %d full calls", eng.fullCalls)
	}
	for _, id := range []string{"dlv-2000", "dlv-2020", "dlv-2039"} {
		if !strings.Contains(storm, id) {
			t.Fatalf("the payout is missing %s, so it is not the full original: %s", id, storm)
		}
	}
	// Having paid out in full, the same handle's full content is now "already
	// served" and a repeat of it points back rather than re-sending.
	again := resultText(t, retrieveTool(eng, session, retrieveArgs("ccr_page1", "")))
	if !strings.Contains(again, "already answered earlier in this session") {
		t.Fatalf("the full original was already given; a repeat must point back: %s", again)
	}
}

// TestRetrieveWithoutASessionBehavesAsBefore is the stateless-safety contract: a
// caller with no session concept keeps the old semantics exactly.
func TestRetrieveWithoutASessionBehavesAsBefore(t *testing.T) {
	eng := newStoreEngine()
	for i := 0; i < retrieveStormThreshold+3; i++ {
		out := resultText(t, retrieveTool(eng, nil, retrieveArgs("ccr_page1", "dlv-2007")))
		if !strings.Contains(out, "dlv-2007") {
			t.Fatalf("retrieve %d returned no content: %s", i+1, out)
		}
		if strings.Contains(out, "caveman: this") {
			t.Fatalf("a session-less retrieve must never emit an anti-storm note: %s", out)
		}
	}
}

// TestAntiStormNeverWithholdsUnseenContent is the honesty gate. The pointer note
// may only ever stand in for bytes this session has already been given.
func TestAntiStormNeverWithholdsUnseenContent(t *testing.T) {
	eng := newStoreEngine()
	session := newRetrieveSession()
	for i := 0; i < retrieveStormThreshold+5; i++ {
		handle, query := "ccr_page1", fmt.Sprintf("dlv-%04d", 2000+i)
		out := resultText(t, retrieveTool(eng, session, retrieveArgs(handle, query)))
		if !strings.Contains(out, "already answered earlier in this session") {
			continue
		}
		t.Fatalf("retrieve %d (%s) was refused but never served: %s", i+1, query, out)
	}
	// A never-requested handle is always served, whatever the session count.
	out := resultText(t, retrieveTool(eng, session, retrieveArgs("ccr_page2", "second")))
	if strings.Contains(out, "already answered") || !strings.Contains(out, "second line") {
		t.Fatalf("an unseen handle must always be served: %s", out)
	}
}
