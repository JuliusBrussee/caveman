package mem

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentRememberGoroutines drives N goroutines against a single file
// store and asserts every write lands. On the pre-fix store (unlimited pool, no
// busy_timeout, no WAL) most writes returned SQLITE_BUSY and were silently
// dropped — 32 concurrent Remember → 1 ok, 31 lost. The single-writer +
// busy_timeout(5000) + WAL discipline serializes them so all N survive.
func TestConcurrentRememberGoroutines(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Remember(fmt.Sprintf("concurrent memory number %d about topic %d", i, i)); err != nil {
				errs <- fmt.Errorf("remember %d: %w", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("%v", err)
	}

	count, err := s.Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != n {
		t.Fatalf("only %d of %d concurrent writes landed", count, n)
	}
}
