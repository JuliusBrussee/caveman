package ccr_test

import (
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

func TestRepositoryMapDedupedAcrossSessions(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	payload := make([]byte, 200000)
	for i := range payload {
		payload[i] = byte(i)
	}

	before, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}

	id1, err := s.PutObject(ccr.Object{
		Type: ccr.ObjectRepositoryMap, Source: "native:repository-map",
		SessionID: "session-a", RepositoryState: "git:abc123",
		TransformVersion: "repository-map-v1", Currentness: ccr.Current,
		Lifecycle: ccr.Warm, Data: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.PutObject(ccr.Object{
		Type: ccr.ObjectRepositoryMap, Source: "native:repository-map",
		SessionID: "session-b", RepositoryState: "git:abc123",
		TransformVersion: "repository-map-v1", Currentness: ccr.Current,
		Lifecycle: ccr.Warm, Data: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatal("expected distinct object ids per session (ListSessionObjects must still find each session's own row)")
	}

	after, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	grown := after.StorageBytes - before.StorageBytes
	if grown >= 2*int64(len(payload)) {
		t.Fatalf("second identical RepositoryMap put duplicated the payload: storage grew by %d bytes for a %d-byte payload (want roughly one copy, not two)", grown, len(payload))
	}

	got1, err := s.GetObject(id1)
	if err != nil || string(got1.Data) != string(payload) {
		t.Fatalf("session-a object did not round-trip byte-exact: err=%v len=%d", err, len(got1.Data))
	}
	got2, err := s.GetObject(id2)
	if err != nil || string(got2.Data) != string(payload) {
		t.Fatalf("session-b object did not round-trip byte-exact: err=%v len=%d", err, len(got2.Data))
	}

	// A third, later session with the SAME content must still dedup against
	// the same underlying bytes, not chain through session-b's row.
	id3, err := s.PutObject(ccr.Object{
		Type: ccr.ObjectRepositoryMap, Source: "native:repository-map",
		SessionID: "session-c", RepositoryState: "git:abc123",
		TransformVersion: "repository-map-v1", Currentness: ccr.Current,
		Lifecycle: ccr.Warm, Data: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	got3, err := s.GetObject(id3)
	if err != nil || string(got3.Data) != string(payload) {
		t.Fatalf("session-c object did not round-trip byte-exact: err=%v len=%d", err, len(got3.Data))
	}
	afterThird, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if afterThird.StorageBytes-after.StorageBytes >= int64(len(payload)) {
		t.Fatalf("third identical RepositoryMap put stored another full copy: storage grew by %d bytes", afterThird.StorageBytes-after.StorageBytes)
	}
}

func TestNonRepositoryMapTypesStillStoreFullCopyPerSession(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	payload := []byte(`{"decision_id":"d1","choice":"same-text-different-sessions"}`)

	before, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject(ccr.Object{
		Type: ccr.ObjectTaskDecision, Source: "native:test", SessionID: "session-a",
		RepositoryState: "git:abc123", Currentness: ccr.Current, Data: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject(ccr.Object{
		Type: ccr.ObjectTaskDecision, Source: "native:test", SessionID: "session-b",
		RepositoryState: "git:abc123", Currentness: ccr.Current, Data: payload,
	}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	grown := after.StorageBytes - before.StorageBytes
	if grown < 2*int64(len(payload)) {
		t.Fatalf("non-RepositoryMap types must keep storing their own full copy per session: storage only grew by %d bytes for two %d-byte puts", grown, len(payload))
	}
}

// TestRepositoryMapDedupedAcrossRepositoryStates covers the other half of
// #1023, and the half a session-scoped dedup key misses entirely.
//
// The object ID hashes (type, session, source, repository_state, content_hash),
// so repository_state alone is enough to mint a new row. The proxy stamps every
// map with request.Session.RepositoryState (nativeruntime/runtime.go), which
// advances on every commit — while the map itself is a structural index that
// most commits leave byte-identical. So one long session over a large repo
// re-stores the same multi-MB map once per commit, with no second session
// involved. On the reporter's db that is 21 rows collapsing to 4 distinct
// content hashes.
func TestRepositoryMapDedupedAcrossRepositoryStates(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	payload := make([]byte, 200000)
	for i := range payload {
		payload[i] = byte(i)
	}

	before, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}

	// One session, three commits, byte-identical map each time.
	states := []string{"git:aaa111", "git:bbb222", "git:ccc333"}
	ids := make([]string, 0, len(states))
	for _, state := range states {
		id, err := s.PutObject(ccr.Object{
			Type: ccr.ObjectRepositoryMap, Source: "native:repository-map",
			SessionID: "one-long-session", RepositoryState: state,
			TransformVersion: "repository-map-v1", Currentness: ccr.Current,
			Lifecycle: ccr.Warm, Data: payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if ids[0] == ids[1] || ids[1] == ids[2] {
		t.Fatal("expected a distinct object id per repository_state (each is its own snapshot row)")
	}

	after, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	grown := after.StorageBytes - before.StorageBytes
	if grown >= 2*int64(len(payload)) {
		t.Fatalf("advancing repository_state duplicated the payload: storage grew by %d bytes for three identical %d-byte maps (want roughly one copy)", grown, len(payload))
	}

	// Every snapshot must still hand back the exact bytes, whichever row owns them.
	for i, id := range ids {
		got, err := s.GetObject(id)
		if err != nil {
			t.Fatalf("snapshot %d (%s): %v", i, states[i], err)
		}
		if string(got.Data) != string(payload) {
			t.Fatalf("snapshot %d (%s) did not round-trip byte-exact: len=%d", i, states[i], len(got.Data))
		}
		if got.RepositoryState != states[i] {
			t.Fatalf("snapshot %d repository_state = %q, want %q — dedup must share bytes, never identity", i, got.RepositoryState, states[i])
		}
	}
}

// TestRepositoryMapDedupSurvivesSessionListing pins the reader path the receipt
// builder depends on: ListSessionObjects resolves a deduped row's data_ref, so
// a session whose map is a pointer still reports the map's real contents rather
// than an empty blob (proxy/internal/nativeruntime/receipt.go unmarshals
// object.Data to count repository files).
func TestRepositoryMapDedupSurvivesSessionListing(t *testing.T) {
	s, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	payload := []byte(`{"files":[{"path":"a.go"},{"path":"b.go"},{"path":"c.go"}]}`)
	for _, session := range []string{"session-first", "session-second"} {
		if _, err := s.PutObject(ccr.Object{
			Type: ccr.ObjectRepositoryMap, Source: "native:repository-map",
			SessionID: session, RepositoryState: "git:abc123",
			TransformVersion: "repository-map-v1", Currentness: ccr.Current,
			Lifecycle: ccr.Warm, Data: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// session-second's row is the pointer; its listing must still carry bytes.
	objects, err := s.ListSessionObjects("session-second", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, object := range objects {
		if object.Type != ccr.ObjectRepositoryMap {
			continue
		}
		found = true
		if string(object.Data) != string(payload) {
			t.Fatalf("listed deduped map data = %q, want the referenced bytes", object.Data)
		}
	}
	if !found {
		t.Fatal("session-second listed no RepositoryMap object")
	}
}
