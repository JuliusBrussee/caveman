package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
)

// storeEngine is the slice of Engine the retrieve path needs, backed by a map of
// stored originals so repeated/query recovery can be exercised without a real store.
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
		if strings.Contains(line, strings.TrimSpace(query)) {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return original, nil
	}
	return []byte(strings.Join(kept, "\n")), nil
}
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

// A host may discard old tool results during compaction while retaining its
// MCP process. Identical requests must still return bytes through the same tools.
func TestRepeatedRetrieveRemainsAvailableAfterHostCompaction(t *testing.T) {
	eng := newStoreEngine()
	tools := EngineTools(eng, nil)
	for _, query := range []string{"dlv-2007", "dlv-2007", "  dlv-2007  "} {
		got := callRetrieveTool(t, tools, "ccr_page1", query)
		if got != "dlv-2007 status=delivered endpoint=alpha" {
			t.Fatalf("repeated query %q did not return requested record: %q", query, got)
		}
	}
	for i := 0; i < 3; i++ {
		if got := callRetrieveTool(t, tools, "ccr_page1", ""); got != eng.originals["ccr_page1"] {
			t.Fatalf("full recovery %d did not preserve exact original: %q", i, got)
		}
	}
	if eng.narrowed != 3 || eng.fullCalls != 3 {
		t.Fatalf("every requested recovery must execute: narrowed=%d full=%d", eng.narrowed, eng.fullCalls)
	}
}

func TestRetrieveQueryDoesNotChangeAfterManyCalls(t *testing.T) {
	eng := newStoreEngine()
	tools := EngineTools(eng, nil)
	for i := 0; i < 12; i++ {
		query := fmt.Sprintf("dlv-%04d", 2000+i)
		got := callRetrieveTool(t, tools, "ccr_page1", query)
		if got != query+" status=delivered endpoint=alpha" {
			t.Fatalf("query %d was denied or widened based on call count: %q", i, got)
		}
	}
	if eng.narrowed != 12 || eng.fullCalls != 0 {
		t.Fatalf("requested query semantics changed: narrowed=%d full=%d", eng.narrowed, eng.fullCalls)
	}
	// The same query and the full original remain available after that sequence.
	if got := callRetrieveTool(t, tools, "ccr_page1", "dlv-2000"); got != "dlv-2000 status=delivered endpoint=alpha" {
		t.Fatalf("earlier query no longer recoverable: %q", got)
	}
	if got := callRetrieveTool(t, tools, "ccr_page1", ""); got != eng.originals["ccr_page1"] {
		t.Fatalf("full original no longer recoverable: %q", got)
	}
	for i := 0; i < 2; i++ {
		result := retrieveTool(eng, retrieveArgs("ccr_unknown", ""))
		if !result.IsError || !strings.Contains(result.Content[0].Text, "cave_unknown_handle") {
			t.Fatalf("unknown handle must always fail explicitly: %+v", result)
		}
	}
}
