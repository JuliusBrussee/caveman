package engine_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

type bestOfCounter struct {
	toonTokens    int
	elisionTokens int
	inputTokens   int
}

func (c bestOfCounter) Name() string { return "best-of-test" }
func (c bestOfCounter) Count(b []byte) int {
	s := string(b)
	switch {
	case strings.Contains(s, "rows["):
		return c.toonTokens
	case strings.Contains(s, "__caveman_elided__"):
		return c.elisionTokens
	default:
		return c.inputTokens
	}
}

func newEngine(t *testing.T) *engine.Engine {
	t.Helper()
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return engine.New(store, nil)
}

const arrayJSON = `{"results":[{"id":1},{"id":2},{"id":3},{"id":4},{"id":5},{"id":6},{"id":7},{"id":8},{"id":9},{"id":10},{"id":11},{"id":12}],"error":null}`

// bestOfJSONInput is a uniform, repetitive array: TOON-eligible, and redundant
// enough that elision is allowed to run at all. Rows that differ in every field
// are a list of distinct entities, which the elision path now declines to shorten
// (see compressors.keepNonRedundant), and these tests are about which method wins
// and what it reports — not about that rule.
const bestOfJSONInput = `{"rows":[{"id":1,"status":"healthy","region":"eu-west-1","tier":"standard"},{"id":2,"status":"healthy","region":"eu-west-1","tier":"standard"},{"id":3,"status":"healthy","region":"eu-west-1","tier":"standard"},{"id":4,"status":"healthy","region":"eu-west-1","tier":"standard"},{"id":5,"status":"healthy","region":"eu-west-1","tier":"standard"},{"id":6,"status":"healthy","region":"eu-west-1","tier":"standard"},{"id":7,"status":"healthy","region":"eu-west-1","tier":"standard"},{"id":8,"status":"healthy","region":"eu-west-1","tier":"standard"},{"id":9,"status":"healthy","region":"eu-west-1","tier":"standard"},{"id":10,"status":"healthy","region":"eu-west-1","tier":"standard"},{"id":11,"status":"healthy","region":"eu-west-1","tier":"standard"},{"id":12,"status":"healthy","region":"eu-west-1","tier":"standard"}]}`

func TestRecordModeIsPassThrough(t *testing.T) {
	e := newEngine(t)
	res, err := e.Compress([]byte(arrayJSON), engine.Options{Mode: engine.ModeRecord})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Output, []byte(arrayJSON)) {
		t.Error("record mode must return bytes unchanged")
	}
	if res.RecoveryHandle != "" || res.Ratio != 0 {
		t.Errorf("record mode must claim nothing: handle=%q ratio=%v", res.RecoveryHandle, res.Ratio)
	}
	if res.Basis != engine.BasisInferred {
		t.Errorf("basis = %q, want %q", res.Basis, engine.BasisInferred)
	}
}

func TestEmptyModeDefaultsToRecord(t *testing.T) {
	e := newEngine(t)
	res, err := e.Compress([]byte(arrayJSON), engine.Options{}) // no mode
	if err != nil {
		t.Fatal(err)
	}
	if !res.PassedThrough() {
		t.Error("empty mode must default to record (pass-through)")
	}
}

func TestUnknownModeFailsClosedToRecord(t *testing.T) {
	e := newEngine(t)
	res, err := e.Compress([]byte(arrayJSON), engine.Options{Mode: engine.Mode("turbo")})
	if err != nil {
		t.Fatal(err)
	}
	if !res.PassedThrough() {
		t.Error("unknown mode must fail closed to record")
	}
}

func TestCompressJSONReducesAndRecovers(t *testing.T) {
	e := newEngine(t)
	res, err := e.Compress([]byte(arrayJSON), engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentType != engine.TypeJSON {
		t.Errorf("content type = %q, want json", res.ContentType)
	}
	if res.PassedThrough() {
		t.Fatal("expected the array JSON to compress")
	}
	if res.Ratio <= 0 || res.TokensAfter >= res.TokensBefore {
		t.Errorf("expected a positive ratio, got %v (%d->%d)", res.Ratio, res.TokensBefore, res.TokensAfter)
	}
	if res.TokenCountBasis == "" {
		t.Error("token_count_basis must disclose estimator")
	}
	if res.Method != "elision" {
		t.Errorf("method = %q, want elision", res.Method)
	}
	if res.LosslessToModel == nil || *res.LosslessToModel {
		t.Errorf("lossless_to_model = %v, want explicit false", res.LosslessToModel)
	}
	// Recovery must return the exact original.
	original, err := e.Retrieve(res.RecoveryHandle)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if !bytes.Equal(original, []byte(arrayJSON)) {
		t.Error("retrieve must return the byte-exact original")
	}
}

func TestRetrieveReturnsValidEmptyTypedObject(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	id, err := store.PutObject(ccr.Object{
		ID:        "typed-empty-output",
		Type:      ccr.ObjectCommandResult,
		SessionID: "empty-output-session",
		Source:    "tool:empty",
		Data:      []byte{},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := engine.New(store, nil).Retrieve(id)
	if err != nil {
		t.Fatalf("retrieve empty typed object: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("retrieve empty typed object = %#v, want zero-byte result", got)
	}
}

func TestBestOfJSONUsesInjectedCounterAndReportsTOON(t *testing.T) {
	t.Setenv("CAVE_ENGINE_TOON", "best-of")
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	counter := bestOfCounter{inputTokens: 100, toonTokens: 10, elisionTokens: 20}
	e := engine.New(store, counter)
	input := []byte(bestOfJSONInput)

	res, err := e.Compress(input, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if res.PassedThrough() {
		t.Fatal("expected best-of compression")
	}
	if res.Method != "toon" {
		t.Fatalf("method = %q, want toon (same injected counter must choose it)", res.Method)
	}
	if res.TokensAfter != 10 {
		t.Fatalf("tokens_after = %d, want injected toon count 10", res.TokensAfter)
	}
	if res.TokenCountBasis != "best-of-test" {
		t.Fatalf("token_count_basis = %q, want best-of-test", res.TokenCountBasis)
	}
	if res.LosslessToModel == nil || !*res.LosslessToModel {
		t.Fatalf("lossless_to_model = %v, want explicit true", res.LosslessToModel)
	}
}

func TestBestOfJSONCanChooseElisionAndReportsExplicitFalse(t *testing.T) {
	t.Setenv("CAVE_ENGINE_TOON", "best-of")
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	counter := bestOfCounter{inputTokens: 100, toonTokens: 20, elisionTokens: 10}
	e := engine.New(store, counter)
	input := []byte(bestOfJSONInput)

	res, err := e.Compress(input, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if res.Method != "elision" {
		t.Fatalf("method = %q, want elision", res.Method)
	}
	if res.LosslessToModel == nil || *res.LosslessToModel {
		t.Fatalf("lossless_to_model = %v, want explicit false", res.LosslessToModel)
	}
}

func TestMalformedInputPassesThrough(t *testing.T) {
	e := newEngine(t)
	bad := []byte(`{"results":[1,2,3,  <-- not json`)
	res, err := e.Compress(bad, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Output, bad) || !res.PassedThrough() {
		t.Error("malformed input must pass through byte-identical with no handle")
	}
}

func TestPlainTextPassesThrough(t *testing.T) {
	e := newEngine(t)
	text := []byte("just an ordinary sentence with nothing to compress")
	res, err := e.Compress(text, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentType != engine.TypeText || !res.PassedThrough() {
		t.Errorf("plain text must detect as text and pass through; got %q passthrough=%v", res.ContentType, res.PassedThrough())
	}
}

func TestCompressIsIdempotent(t *testing.T) {
	e := newEngine(t)
	first, err := e.Compress([]byte(arrayJSON), engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Compress(first.Output, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second.Output, first.Output) {
		t.Errorf("compress must be idempotent:\n first=%s\nsecond=%s", first.Output, second.Output)
	}
}

func TestNilStoreFailsClosedNoLossyCompression(t *testing.T) {
	// Without a recovery store, a lossy (S4) result would be unrecoverable, so
	// the engine must pass through rather than compress.
	e := engine.New(nil, nil)
	res, err := e.Compress([]byte(arrayJSON), engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if !res.PassedThrough() {
		t.Error("with no CCR store, lossy compression must fail closed to pass-through")
	}
}

func TestCCRWriteFailureReturnsPassThroughResult(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(store, nil)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	input := []byte(arrayJSON)
	res, err := e.Compress(input, engine.Options{Mode: engine.ModeCompress})
	if err == nil {
		t.Fatal("closed CCR store must fail the compression")
	}
	if !bytes.Equal(res.Output, input) || res.RecoveryHandle != "" || res.Method != "" ||
		res.TokensAfter != res.TokensBefore || res.Ratio != 0 {
		t.Fatalf("CCR failure must return an untouched pass-through result: %+v", res)
	}
}

func TestExternalRecoveryAllowsS4WithoutLocalCCRHandle(t *testing.T) {
	e := engine.New(nil, nil)
	res, err := e.Compress([]byte(arrayJSON), engine.Options{Mode: engine.ModeCompress, ExternalRecovery: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.PassedThrough() {
		t.Fatal("external recovery caller should be able to emit S4 output")
	}
	if res.RecoveryHandle != "" {
		t.Fatalf("external recovery must not create local CCR handle, got %q", res.RecoveryHandle)
	}
	if res.Method != "elision" {
		t.Fatalf("method = %q, want elision", res.Method)
	}
}

// TestRetrieveQueryNarrowsToRelevantSections checks query-targeted recovery
// path: empty query is byte-exact, a query returns only the BM25-relevant sections,
// and an unknown handle still fails closed.
func TestRetrieveQueryNarrowsToRelevantSections(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e := engine.New(store, nil)

	original := []byte(`{"model":"m","messages":[{"role":"user","content":"Section about kubernetes pod scheduling and node affinity."},{"role":"user","content":"Section about postgres vacuum tuning and autovacuum thresholds."},{"role":"user","content":"Section about redis eviction policies and maxmemory."}]}`)
	handle, err := store.Put(ccr.Recovery{ContentType: "request", Compressor: "proxy-content", Original: original})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	full, err := e.RetrieveQuery(handle, "")
	if err != nil {
		t.Fatalf("full retrieve: %v", err)
	}
	if !bytes.Equal(full, original) {
		t.Errorf("empty query must return the byte-exact original")
	}

	narrowed, err := e.RetrieveQuery(handle, "postgres autovacuum tuning")
	if err != nil {
		t.Fatalf("query retrieve: %v", err)
	}
	if !strings.Contains(string(narrowed), "vacuum") {
		t.Errorf("query-targeted retrieve must include the relevant section, got: %s", narrowed)
	}
	if strings.Contains(string(narrowed), "kubernetes") || strings.Contains(string(narrowed), "redis eviction") {
		t.Errorf("query-targeted retrieve must drop irrelevant sections, got: %s", narrowed)
	}
	if len(narrowed) >= len(full) {
		t.Errorf("narrowed (%d bytes) must be smaller than full recovery (%d bytes)", len(narrowed), len(full))
	}

	if _, err := e.RetrieveQuery("no_such_handle", "anything"); err == nil {
		t.Errorf("unknown handle must fail closed, got nil error")
	}
}
