package middleware

import (
	"crypto/sha256"
	"sync"

	"github.com/JuliusBrussee/caveman/engine/tokens"
)

// Repeated manifests and summaries need the same exact Engine token count.
// Cache only digests and counts, never source text. In-flight counts coalesce;
// independent content remains concurrent. Eviction changes speed, not results.
type counted struct {
	ready chan struct{}
	n     int
}

type memoCounter struct {
	inner tokens.Counter
	mu    sync.Mutex
	items map[[32]byte]*counted
}

func newMemoCounter(inner tokens.Counter) *memoCounter {
	return &memoCounter{inner: inner, items: make(map[[32]byte]*counted)}
}

func (c *memoCounter) Name() string { return c.inner.Name() }

func (c *memoCounter) Count(b []byte) int {
	key := sha256.Sum256(b)
	c.mu.Lock()
	if old, ok := c.items[key]; ok {
		c.mu.Unlock()
		<-old.ready
		return old.n
	}
	if len(c.items) >= 4096 {
		for key, old := range c.items {
			select {
			case <-old.ready:
				delete(c.items, key)
			default:
			}
			if len(c.items) < 3072 {
				break
			}
		}
	}
	entry := &counted{ready: make(chan struct{})}
	c.items[key] = entry
	c.mu.Unlock()
	entry.n = c.inner.Count(b)
	close(entry.ready)
	return entry.n
}
