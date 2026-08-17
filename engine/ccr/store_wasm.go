//go:build js && wasm

// The js/wasm recovery store: a pure-Go in-memory map. The browser has no
// filesystem and modernc.org/sqlite does not build for js/wasm, so recoveries
// live in process memory and are session-scoped (lost when the page unloads).
// It exposes the same type and methods as the SQLite store (store_sqlite.go).
package ccr

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

type record struct {
	contentType  string
	tokensBefore int
	tokensAfter  int
	original     []byte
	metadata     []byte
}

// Store is an in-memory recovery store.
type Store struct {
	mu      sync.Mutex
	recs    map[string]record
	objects map[string]Object
}

// Open ignores path (there is no filesystem) and returns an in-memory store.
func Open(string) (*Store, error) { return OpenMemory() }

// OpenWithBudget mirrors the host API. WASM storage is session-memory only;
// browser-host quota enforcement belongs to the embedding host.
func OpenWithBudget(string, int64) (*Store, error) { return OpenMemory() }

// OpenMemory returns a fresh in-memory store.
func OpenMemory() (*Store, error) {
	return &Store{recs: map[string]record{}, objects: map[string]Object{}}, nil
}

// Close is a no-op.
func (s *Store) Close() error { return nil }

// Put stores a recovery and returns its content-addressed handle (idempotent).
func (s *Store) Put(rec Recovery) (string, error) {
	handle := Handle(rec.Original)
	s.mu.Lock()
	defer s.mu.Unlock()
	metadata := append([]byte(nil), rec.Metadata...)
	existing, exists := s.recs[handle]
	if exists && len(existing.metadata) > 0 {
		metadata = append([]byte(nil), existing.metadata...)
	}
	if exists && rec.TokensBefore == 0 && existing.tokensBefore > 0 {
		rec.ContentType = existing.contentType
		rec.TokensBefore = existing.tokensBefore
		rec.TokensAfter = existing.tokensAfter
	}
	s.recs[handle] = record{
		contentType:  rec.ContentType,
		tokensBefore: rec.TokensBefore,
		tokensAfter:  rec.TokensAfter,
		original:     append([]byte(nil), rec.Original...),
		metadata:     metadata,
	}
	return handle, nil
}

// Get returns the exact original bytes for a handle, or ErrNotFound.
func (s *Store) Get(handle string) ([]byte, error) {
	if strings.HasPrefix(handle, "ccr_obj_") {
		obj, err := s.GetObject(handle)
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), obj.Data...), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[handle]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), r.original...), nil
}

// GetMetadata returns optional compressor metadata stored with a handle. A known
// handle with no metadata returns nil, nil; an unknown handle returns ErrNotFound.
func (s *Store) GetMetadata(handle string) ([]byte, error) {
	if strings.HasPrefix(handle, "ccr_obj_") {
		if _, err := s.GetObject(handle); err != nil {
			return nil, err
		}
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[handle]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), r.metadata...), nil
}

func cloneObject(obj Object) Object {
	obj.Dependencies = append([]string(nil), obj.Dependencies...)
	obj.Data = bytes.Clone(obj.Data)
	return obj
}

func (s *Store) PutObject(input Object) (string, error) {
	obj, err := prepareObject(input)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, exists := s.objects[obj.ID]; exists {
		if !sameImmutableObject(existing, obj) {
			return "", errors.New("ccr: typed object id collision")
		}
	} else {
		s.objects[obj.ID] = cloneObject(obj)
	}
	return obj.ID, nil
}

func (s *Store) GetObject(id string) (Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	obj, ok := s.objects[id]
	if !ok {
		return Object{}, ErrNotFound
	}
	return cloneObject(obj), nil
}

func (s *Store) SetObjectCurrentness(id string, currentness Currentness) error {
	if err := validateCurrentness(currentness); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	obj, ok := s.objects[id]
	if !ok {
		return ErrNotFound
	}
	obj.Currentness = currentness
	s.objects[id] = obj
	return nil
}

func (s *Store) SetObjectLifecycle(id string, lifecycle Lifecycle) error {
	if err := validateLifecycle(lifecycle); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	obj, ok := s.objects[id]
	if !ok {
		return ErrNotFound
	}
	obj.Lifecycle = lifecycle
	s.objects[id] = obj
	return nil
}

func (s *Store) ListSessionObjects(sessionID string, limit int) ([]Object, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 10000 {
		limit = 10000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	objects := make([]Object, 0)
	for _, obj := range s.objects {
		if obj.SessionID == sessionID {
			objects = append(objects, cloneObject(obj))
		}
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].CreatedAt.Equal(objects[j].CreatedAt) {
			return objects[i].ID > objects[j].ID
		}
		return objects[i].CreatedAt.After(objects[j].CreatedAt)
	})
	if len(objects) > limit {
		objects = objects[:limit]
	}
	return objects, nil
}

func (s *Store) FindTaskDecision(decisionID string) (Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, obj := range s.objects {
		if obj.Type != ObjectTaskDecision {
			continue
		}
		var decision struct {
			ID string `json:"decision_id"`
		}
		if json.Unmarshal(obj.Data, &decision) == nil && decision.ID == decisionID {
			return cloneObject(obj), nil
		}
	}
	return Object{}, ErrNotFound
}

// Summary aggregates the in-memory recoveries into totals + per-type buckets.
func (s *Store) Summary() (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Stats{ByContentType: map[string]Bucket{}, Basis: "inferred"}
	for _, r := range s.recs {
		b := out.ByContentType[r.contentType]
		b.Count++
		b.TokensBefore += r.tokensBefore
		b.TokensAfter += r.tokensAfter
		b.Ratio = ratio(b.TokensBefore, b.TokensAfter)
		out.ByContentType[r.contentType] = b
		out.Totals.Count++
		out.Totals.TokensBefore += r.tokensBefore
		out.Totals.TokensAfter += r.tokensAfter
	}
	out.Totals.Ratio = ratio(out.Totals.TokensBefore, out.Totals.TokensAfter)
	return out, nil
}
