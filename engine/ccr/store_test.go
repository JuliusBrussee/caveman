package ccr_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

func TestPutGetByteExact(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	original := []byte(`{"a":[1,2,3],"b":"value"}`)
	handle, err := s.Put(ccr.Recovery{ContentType: "json", Compressor: "json", TokensBefore: 100, TokensAfter: 20, Original: original})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Error("Get must return the byte-exact original")
	}
}

func TestTypedObjectRoundTripAndCurrentness(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	created := time.Date(2026, 8, 8, 12, 0, 0, 123, time.UTC)
	id, err := s.PutObject(ccr.Object{
		Type:               ccr.ObjectFileObservation,
		Source:             "src/auth.ts:10-30",
		CreatedAt:          created,
		RepositoryState:    "git:abc123:index:def456",
		SessionID:          "session-1",
		TransformVersion:   "native-runtime-v1",
		Currentness:        ccr.Current,
		Dependencies:       []string{"src/auth.ts", "package-lock.json"},
		OriginalByteLength: 14,
		StoredByteLength:   14,
		Data:               []byte("exact contents"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("typed object must receive an id")
	}
	got, err := s.GetObject(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != ccr.ObjectFileObservation || got.SessionID != "session-1" || got.Currentness != ccr.Current {
		t.Fatalf("typed object metadata mismatch: %+v", got)
	}
	if !got.CreatedAt.Equal(created) || !bytes.Equal(got.Data, []byte("exact contents")) {
		t.Fatalf("typed object exact data mismatch: %+v", got)
	}
	if len(got.Dependencies) != 2 || got.Dependencies[0] != "src/auth.ts" {
		t.Fatalf("typed object dependencies mismatch: %+v", got.Dependencies)
	}
	if err := s.SetObjectCurrentness(id, ccr.Stale); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetObject(id)
	if err != nil || got.Currentness != ccr.Stale {
		t.Fatalf("currentness update failed: currentness=%q err=%v", got.Currentness, err)
	}
	if err := s.SetObjectLifecycle(id, ccr.Cold); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetObject(id)
	if err != nil || got.Lifecycle != ccr.Cold {
		t.Fatalf("lifecycle update failed: lifecycle=%q err=%v", got.Lifecycle, err)
	}
	if err := s.SetObjectLifecycle(id, "future"); err == nil {
		t.Fatal("unknown lifecycle must fail closed")
	}
}

func TestPutObjectRejectsImmutableIDCollision(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const id = "ccr_obj_fixed"
	first := ccr.Object{
		ID:        id,
		Type:      ccr.ObjectCommandResult,
		SessionID: "session-a",
		Source:    "tool-a",
		Data:      []byte("first result"),
	}
	if _, err := s.PutObject(first); err != nil {
		t.Fatalf("first put: %v", err)
	}

	second := first
	second.Data = []byte("different result")
	if _, err := s.PutObject(second); err == nil {
		t.Fatal("immutable object ID collision must fail closed")
	}

	stored, err := s.GetObject(id)
	if err != nil {
		t.Fatalf("get original: %v", err)
	}
	if got, want := string(stored.Data), string(first.Data); got != want {
		t.Fatalf("collision changed stored bytes: got %q want %q", got, want)
	}
}

func TestTypedObjectFailsClosedAndIsSessionScoped(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.PutObject(ccr.Object{Type: "FutureUnknownObject", SessionID: "s1", Data: []byte("x")}); err == nil {
		t.Fatal("unknown object type must fail closed")
	}
	if _, err := s.PutObject(ccr.Object{Type: ccr.ObjectCommandResult, SessionID: "s1", Currentness: "maybe", Data: []byte("x")}); err == nil {
		t.Fatal("unknown currentness must fail closed")
	}
	for _, session := range []string{"s1", "s2"} {
		if _, err := s.PutObject(ccr.Object{Type: ccr.ObjectCommandResult, SessionID: session, Currentness: ccr.Current, Source: session, Data: []byte(session)}); err != nil {
			t.Fatal(err)
		}
	}
	objects, err := s.ListSessionObjects("s1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].SessionID != "s1" || string(objects[0].Data) != "s1" {
		t.Fatalf("session-scoped listing leaked objects: %+v", objects)
	}
}

func TestFindTaskDecision(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	data := []byte(`{"schema":"caveman.native.decision.v1","decision_id":"dec_0123456789abcdef01234567","reason":"test"}`)
	if _, err := s.PutObject(ccr.Object{Type: ccr.ObjectTaskDecision, SessionID: "s1", Source: "native:test", Data: data}); err != nil {
		t.Fatal(err)
	}
	got, err := s.FindTaskDecision("dec_0123456789abcdef01234567")
	if err != nil || string(got.Data) != string(data) {
		t.Fatalf("decision lookup failed: object=%+v err=%v", got, err)
	}
	if _, err := s.FindTaskDecision("dec_missing"); !errors.Is(err, ccr.ErrNotFound) {
		t.Fatalf("missing decision error = %v, want ErrNotFound", err)
	}
}

func TestTypedObjectPersistsOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	s, err := ccr.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.PutObject(ccr.Object{Type: ccr.ObjectTaskContract, SessionID: "persist", Currentness: ccr.Current, Data: []byte(`{"goal":"ship"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = ccr.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetObject(id)
	if err != nil || string(got.Data) != `{"goal":"ship"}` {
		t.Fatalf("reopened store lost typed object: got=%q err=%v", got.Data, err)
	}
}

func TestPutGetMetadata(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	meta := []byte(`{"uids":{"u16":{"backendDOMNodeId":42}}}`)
	handle, err := s.Put(ccr.Recovery{ContentType: "a11y", Compressor: "a11y", Original: []byte("raw"), Metadata: meta})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMetadata(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, meta) {
		t.Fatalf("metadata mismatch: got=%s want=%s", got, meta)
	}
	if original, err := s.Get(handle); err != nil || string(original) != "raw" {
		t.Fatalf("metadata must not change byte-exact Get: got=%q err=%v", original, err)
	}
	if _, err := s.GetMetadata("ccr_missing"); !errors.Is(err, ccr.ErrNotFound) {
		t.Fatalf("unknown metadata handle must fail closed, got %v", err)
	}
}

func TestMetadataPreservedAcrossIdempotentPuts(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	original := []byte(`{"nodes":[{"nodeId":"1","role":{"value":"RootWebArea"}}]}`)
	meta := []byte(`{"uids":{"u16":{"backendDOMNodeId":42}}}`)
	handle, err := s.Put(ccr.Recovery{ContentType: "a11y", Compressor: "a11y", Original: original, Metadata: meta})
	if err != nil {
		t.Fatal(err)
	}
	handle2, err := s.Put(ccr.Recovery{ContentType: "json", Compressor: "json", Original: original})
	if err != nil {
		t.Fatal(err)
	}
	if handle2 != handle {
		t.Fatalf("same original should keep same handle: %q != %q", handle2, handle)
	}
	got, err := s.GetMetadata(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, meta) {
		t.Fatalf("idempotent put erased metadata: got=%s want=%s", got, meta)
	}
	different := []byte(`{"other":true}`)
	if _, err := s.Put(ccr.Recovery{ContentType: "a11y", Compressor: "a11y", Original: original, Metadata: different}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetMetadata(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, meta) {
		t.Fatalf("later metadata should not replace first action map: got=%s want=%s", got, meta)
	}
}

func TestHandleIsContentAddressedAndIdempotent(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	original := []byte("same bytes")
	h1, _ := s.Put(ccr.Recovery{ContentType: "log", Compressor: "log", Original: original})
	h2, _ := s.Put(ccr.Recovery{ContentType: "log", Compressor: "log", Original: original})
	if h1 != h2 {
		t.Errorf("same bytes must yield the same handle: %q != %q", h1, h2)
	}
	if h1 != ccr.Handle(original) {
		t.Error("handle must be the content-addressed Handle()")
	}
}

func TestIdempotentZeroAccountingPutCannotEraseMeasuredStats(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	original := []byte(`{"items":[1,2,3]}`)
	if _, err := s.Put(ccr.Recovery{ContentType: "json", Compressor: "json", TokensBefore: 100, TokensAfter: 25, Original: original}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ccr.Recovery{ContentType: "block", Compressor: "proxy-content", Original: original}); err != nil {
		t.Fatal(err)
	}
	stats, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Totals.Count != 1 || stats.Totals.TokensBefore != 100 || stats.Totals.TokensAfter != 25 || stats.ByContentType["json"].Count != 1 {
		t.Fatalf("idempotent zero-accounting put erased measured stats: %+v", stats)
	}
}

func TestGetUnknownHandleFailsClosed(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Get("ccr_does_not_exist"); !errors.Is(err, ccr.ErrNotFound) {
		t.Errorf("unknown handle must return ErrNotFound, got %v", err)
	}
}

func TestSummary(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Put(ccr.Recovery{ContentType: "json", Compressor: "json", TokensBefore: 100, TokensAfter: 20, Original: []byte("a")})
	s.Put(ccr.Recovery{ContentType: "log", Compressor: "log", TokensBefore: 200, TokensAfter: 10, Original: []byte("b")})
	stats, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Totals.Count != 2 || stats.Totals.TokensBefore != 300 || stats.Totals.TokensAfter != 30 {
		t.Errorf("totals wrong: %+v", stats.Totals)
	}
	if stats.Basis != "inferred" {
		t.Errorf("basis = %q, want inferred", stats.Basis)
	}
	if _, ok := stats.ByContentType["json"]; !ok {
		t.Error("expected a json bucket")
	}
}

func TestOnDiskPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	s, err := ccr.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := s.Put(ccr.Recovery{ContentType: "json", Compressor: "json", Original: []byte("persist me")})
	s.Close()

	s2, err := ccr.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Get(handle)
	if err != nil || string(got) != "persist me" {
		t.Errorf("reopened store lost data: got=%q err=%v", got, err)
	}
}

func TestPersistentStoreBudgetRefusesNewRecoveryAndPreservesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr-budget.db")
	const budget = int64(1 << 20)
	store, err := ccr.OpenWithBudget(path, budget)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := bytes.Repeat([]byte("a"), 600<<10)
	handle, err := store.Put(ccr.Recovery{ContentType: "text", Compressor: "test", Original: first, TokensBefore: 10, TokensAfter: 5})
	if err != nil {
		t.Fatalf("first recovery: %v", err)
	}
	second := bytes.Repeat([]byte("b"), 600<<10)
	if _, err := store.Put(ccr.Recovery{ContentType: "text", Compressor: "test", Original: second, TokensBefore: 10, TokensAfter: 5}); !errors.Is(err, ccr.ErrBudgetExceeded) {
		t.Fatalf("second recovery error = %v, want ErrBudgetExceeded", err)
	}
	got, err := store.Get(handle)
	if err != nil || !bytes.Equal(got, first) {
		t.Fatalf("existing recovery lost after quota refusal: err=%v", err)
	}
	if same, err := store.Put(ccr.Recovery{ContentType: "text", Compressor: "test", Original: first, TokensBefore: 10, TokensAfter: 5}); err != nil || same != handle {
		t.Fatalf("idempotent repeat at quota: handle=%q err=%v", same, err)
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.StorageBytes != int64(len(first)) || stats.MaxStorageBytes != budget || stats.StorageFull {
		t.Fatalf("storage stats = %+v", stats)
	}
}
