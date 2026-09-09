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
