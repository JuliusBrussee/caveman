package objectstore

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Memory is an in-memory Store for tests (no network). It is safe for
// concurrent use.
type Memory struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemory returns an empty in-memory Store.
func NewMemory() *Memory { return &Memory{data: map[string][]byte{}} }

func (m *Memory) Put(_ context.Context, key string, body []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(body))
	copy(cp, body)
	m.data[key] = cp
	return nil
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *Memory) DeleteAllVersions(ctx context.Context, key string) error {
	return m.Delete(ctx, key)
}

func (m *Memory) DeletePrefixAllVersions(_ context.Context, prefix string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.data {
		if strings.HasPrefix(key, prefix) {
			delete(m.data, key)
		}
	}
	return nil
}

func (m *Memory) Exists(_ context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *Memory) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *Memory) ListPage(_ context.Context, prefix, startAfter string, maxKeys int) (ObjectPage, error) {
	if maxKeys <= 0 {
		return ObjectPage{}, ErrPagingUnsupported
	}
	m.mu.RLock()
	keys := make([]string, 0, len(m.data))
	for key := range m.data {
		if strings.HasPrefix(key, prefix) && key > startAfter {
			keys = append(keys, key)
		}
	}
	m.mu.RUnlock()
	sort.Strings(keys)
	if len(keys) <= maxKeys {
		return ObjectPage{Keys: keys}, nil
	}
	return ObjectPage{Keys: keys[:maxKeys], NextToken: keys[maxKeys-1], Truncated: true}, nil
}

// Len reports how many objects are stored (test helper for ZDR assertions).
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.data)
}
