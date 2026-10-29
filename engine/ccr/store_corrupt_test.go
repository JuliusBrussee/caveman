//go:build !js

package ccr

import (
	"errors"
	"testing"
)

func putRepositoryMap(t *testing.T, s *Store, session string, data []byte) string {
	t.Helper()
	id, err := s.PutObject(Object{
		Type: ObjectRepositoryMap, Source: "native:repository-map",
		SessionID: session, RepositoryState: "git:abc123",
		TransformVersion: "repository-map-v1", Currentness: Current,
		Lifecycle: Warm, Data: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGetRefusesBlobThatDoesNotMatchHandle(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	original := []byte("exact original bytes")
	handle, err := s.Put(Recovery{Original: original, ContentType: "text"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(handle)
	if err != nil || string(got) != string(original) {
		t.Fatalf("unchanged row must return exact bytes: %q %v", got, err)
	}
	if _, err := s.db.Exec(`UPDATE recoveries SET original = ? WHERE handle = ?`, []byte("damaged bytes"), handle); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(handle); !errors.Is(err, ErrCorruptRecovery) {
		t.Fatalf("Get err = %v, want ErrCorruptRecovery", err)
	}
	if errors.Is(ErrCorruptRecovery, ErrNotFound) {
		t.Fatal("corruption must not read as not found")
	}
}

func TestGetObjectRefusesDataThatDoesNotMatchContentHash(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.PutObject(Object{
		Type: ObjectFileObservation, SessionID: "corrupt", Source: "test",
		Data: []byte("exact object bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if obj, err := s.GetObject(id); err != nil || string(obj.Data) != "exact object bytes" {
		t.Fatalf("unchanged object must return exact bytes: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE typed_objects SET data = ? WHERE object_id = ?`, []byte("damaged"), id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetObject(id); !errors.Is(err, ErrCorruptRecovery) {
		t.Fatalf("GetObject err = %v, want ErrCorruptRecovery", err)
	}
	if _, err := s.Get(id); !errors.Is(err, ErrCorruptRecovery) {
		t.Fatalf("Get of object id err = %v, want ErrCorruptRecovery", err)
	}
	if _, err := s.ListSessionObjects("corrupt", 10); !errors.Is(err, ErrCorruptRecovery) {
		t.Fatalf("ListSessionObjects err = %v, want ErrCorruptRecovery", err)
	}
}

func TestDedupReferenceRefusesDamagedOwnerData(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i)
	}
	owner := putRepositoryMap(t, s, "session-a", payload)
	ref := putRepositoryMap(t, s, "session-b", payload)
	if got, err := s.GetObject(ref); err != nil || string(got.Data) != string(payload) {
		t.Fatalf("unchanged reference must return exact bytes: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE typed_objects SET data = ? WHERE object_id = ?`, []byte("damaged"), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetObject(ref); !errors.Is(err, ErrCorruptRecovery) {
		t.Fatalf("GetObject of reference err = %v, want ErrCorruptRecovery", err)
	}
	if _, err := s.ListSessionObjects("session-b", 10); !errors.Is(err, ErrCorruptRecovery) {
		t.Fatalf("ListSessionObjects of reference err = %v, want ErrCorruptRecovery", err)
	}
}
