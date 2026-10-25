//go:build !js

package ccr

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentRepositoryMapPutsKeepOnePayloadOwner(t *testing.T) {
	const writers = 12
	path := filepath.Join(t.TempDir(), "recoveries.db")
	stores := make([]*Store, writers)
	for i := range stores {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = store
		t.Cleanup(func() { _ = store.Close() })
	}
	for round := range 3 {
		payload := bytes.Repeat([]byte(fmt.Sprintf("repository map %d\n", round)), 4096)
		start := make(chan struct{})
		ids := make([]string, writers)
		errs := make([]error, writers)
		var wg sync.WaitGroup
		for i, store := range stores {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				ids[i], errs[i] = store.PutObject(Object{
					Type: ObjectRepositoryMap, Source: "native:repository-map",
					SessionID: fmt.Sprintf("session-%d", i), RepositoryState: fmt.Sprintf("git:%d", round),
					TransformVersion: "repository-map-v1", Currentness: Current,
					Lifecycle: Warm, Data: payload,
				})
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("writer %d: %v", i, err)
			}
			obj, err := stores[i].GetObject(ids[i])
			if err != nil || !bytes.Equal(obj.Data, payload) {
				t.Fatalf("writer %d round trip: err=%v bytes=%d", i, err, len(obj.Data))
			}
		}
		var owners, invalidRefs int
		if err := stores[0].db.QueryRow(`SELECT COUNT(*) FROM typed_objects WHERE content_hash = (SELECT content_hash FROM typed_objects WHERE object_id = ?) AND data_ref = ''`, ids[0]).Scan(&owners); err != nil {
			t.Fatal(err)
		}
		if err := stores[0].db.QueryRow(`SELECT COUNT(*) FROM typed_objects AS child LEFT JOIN typed_objects AS owner ON child.data_ref = owner.object_id WHERE child.data_ref != '' AND (owner.object_id IS NULL OR owner.data_ref != '')`).Scan(&invalidRefs); err != nil {
			t.Fatal(err)
		}
		if owners != 1 || invalidRefs != 0 {
			t.Fatalf("round %d: full payload owners=%d, invalid references=%d; want one owner and direct references", round, owners, invalidRefs)
		}
	}
}

func TestWriteTransactionReleasesConnectionAfterRefusal(t *testing.T) {
	for _, object := range []bool{false, true} {
		for _, physical := range []bool{false, true} {
			t.Run(fmt.Sprintf("object=%t/physical=%t", object, physical), func(t *testing.T) {
				store, err := OpenWithBudget(filepath.Join(t.TempDir(), "recoveries.db"), 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				put := func(data []byte) (string, error) {
					if object {
						return store.PutObject(Object{Type: ObjectFileObservation, Source: "test", SessionID: "refusal", Data: data})
					}
					return store.Put(Recovery{Original: data, ContentType: "text"})
				}
				payloadSize := 2 << 20
				var maxPages int
				if physical {
					var pages int
					if err := store.db.QueryRow("PRAGMA max_page_count").Scan(&maxPages); err != nil {
						t.Fatal(err)
					}
					if err := store.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
						t.Fatal(err)
					}
					if _, err := store.db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", pages)); err != nil {
						t.Fatal(err)
					}
					payloadSize = 512 << 10
				}
				if id, err := put(bytes.Repeat([]byte("x"), payloadSize)); id != "" || !errors.Is(err, ErrBudgetExceeded) {
					t.Fatalf("refused put = %q, %v; want no published handle and ErrBudgetExceeded", id, err)
				}
				if physical {
					if _, err := store.db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", maxPages)); err != nil {
						t.Fatal(err)
					}
				}
				payload := []byte("small recovery after refusal")
				id, err := put(payload)
				if err != nil {
					t.Fatalf("connection unusable after refusal: %v", err)
				}
				if same, err := put(payload); err != nil || same != id {
					t.Fatalf("idempotent put = %q, %v; want %q", same, err, id)
				}
				if got, err := store.Get(id); err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("post-refusal recovery = %q, %v", got, err)
				}
			})
		}
	}
}
